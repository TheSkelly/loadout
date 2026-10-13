package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Remote: a small phone page served by Loadout itself, so builds and
// transfers can be watched (and started or stopped) away from the PC. It is
// off by default, needs a PIN, and only offers progress, build, send and stop:
// nothing on it can rename, delete or change settings.

const defaultRemotePort = 8787

// RemoteInfo is what Settings shows about the phone page.
type RemoteInfo struct {
	On        bool     `json:"on"`
	Running   bool     `json:"running"`
	Port      int      `json:"port"`
	URLs      []string `json:"urls"`      // every address the page answers on
	Tailscale string   `json:"tailscale"` // the Tailscale address, if this PC has one
	Err       string   `json:"err"`
	HasPIN    bool     `json:"hasPin"`
}

type remoteServer struct {
	srv   *http.Server
	port  int
	token string // session cookie value, new each time the server starts

	mu       sync.Mutex
	fails    int
	lockedTo time.Time
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// localAddrs lists this PC's IPv4 addresses, Tailscale's first.
func localAddrs() (all []string, tailscale string) {
	_, ts, _ := net.ParseCIDR("100.64.0.0/10")
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ts.Contains(ip) {
			tailscale = ip.String()
			continue
		}
		all = append(all, ip.String())
	}
	sort.Strings(all)
	if tailscale != "" {
		all = append([]string{tailscale}, all...)
	}
	return all, tailscale
}

func (a *App) remoteInfoLocked() RemoteInfo {
	ri := RemoteInfo{On: a.cfg.RemoteOn, Port: a.cfg.RemotePort, Err: a.remoteErr, HasPIN: len(a.cfg.RemotePIN) >= 4, URLs: []string{}}
	if ri.Port == 0 {
		ri.Port = defaultRemotePort
	}
	ri.Running = a.remote != nil
	if ri.Running {
		ips, ts := localAddrs()
		for _, ip := range ips {
			ri.URLs = append(ri.URLs, fmt.Sprintf("http://%s:%d", ip, ri.Port))
		}
		if ts != "" {
			ri.Tailscale = fmt.Sprintf("http://%s:%d", ts, ri.Port)
		}
	}
	return ri
}

// applyRemote starts, restarts or stops the phone page to match the settings.
func (a *App) applyRemote() {
	a.mu.Lock()
	on, port, pin := a.cfg.RemoteOn, a.cfg.RemotePort, a.cfg.RemotePIN
	old := a.remote
	if port == 0 {
		port = defaultRemotePort
	}
	if old != nil && on && old.port == port {
		a.mu.Unlock()
		return // already serving on the right port; the PIN is read live
	}
	a.remote, a.remoteErr = nil, ""
	a.mu.Unlock()
	if old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		old.srv.Shutdown(ctx)
		cancel()
	}
	if !on {
		return
	}
	if len(pin) < 4 {
		a.mu.Lock()
		a.remoteErr = "Set a PIN of at least 4 digits to turn the phone page on."
		a.mu.Unlock()
		return
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		a.mu.Lock()
		a.remoteErr = fmt.Sprintf("Port %d is already in use on this PC. Pick another port.", port)
		a.mu.Unlock()
		return
	}
	rs := &remoteServer{port: port, token: randHex(24)}
	rs.srv = &http.Server{Handler: a.remoteHandler(rs), ReadHeaderTimeout: 10 * time.Second}
	a.mu.Lock()
	a.remote = rs
	a.mu.Unlock()
	go rs.srv.Serve(ln)
}

// ---------- what the phone sees ----------

type remoteLane struct {
	Busy  bool   `json:"busy"`
	What  string `json:"what"`
	Pct   int    `json:"pct"` // 0..1000 for the whole lane
	Speed int64  `json:"speed"`
	ETA   int64  `json:"eta"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

type remoteJob struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Pct   int    `json:"pct"`
	Note  string `json:"note"`
}

type remoteGame struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	ID       string `json:"id"`
	Size     int64  `json:"size"`
	Cover    string `json:"cover"`
	HasImage bool   `json:"hasImage"`
	CanBuild bool   `json:"canBuild"`
	OnPS5    string `json:"onPs5"`
	Busy     bool   `json:"busy"`
}

type remoteState struct {
	Version  string          `json:"version"`
	PC       string          `json:"pc"`
	Build    remoteLane      `json:"build"`
	PS5      remoteLane      `json:"ps5"`
	Jobs     []remoteJob     `json:"jobs"`
	Summary  string          `json:"summary"`
	Online   bool            `json:"ps5Online"`
	HasPS5   bool            `json:"hasPs5"`
	Drives   []PS5Drive      `json:"drives"`
	Drive    string          `json:"drive"`
	Games    []remoteGame    `json:"games"`
	Payloads []remotePayload `json:"payloads"`
}

type remotePayload struct {
	I       int    `json:"i"`
	Name    string `json:"name"`
	Known   string `json:"known"`
	Size    int64  `json:"size"`
	Missing bool   `json:"missing"`
}

func ps5JobState(j *BuildJob) string {
	if j.Kind == "build" {
		return j.SendState
	}
	return j.State
}

func (a *App) remoteStateLocked() remoteState {
	st := a.stateLocked()
	// reading state for the phone must not swallow the desktop's toasts
	a.toast = append(st.Toasts, a.toast...)
	host, _ := os.Hostname()
	rs := remoteState{Version: appVersion, PC: host, Summary: a.build.Summary, Online: a.ps5info.Online,
		HasPS5: strings.TrimSpace(a.cfg.PS5IP) != "", Drives: st.PS5.Drives, Drive: rootOf(a.cfg.PS5Dir), Jobs: []remoteJob{}, Games: []remoteGame{}, Payloads: []remotePayload{}}
	for i, p := range st.Payloads {
		rs.Payloads = append(rs.Payloads, remotePayload{I: i, Name: p.Name, Known: p.Known, Size: p.Size, Missing: p.Missing})
	}
	fin := map[string]bool{"done": true, "sent": true, "failed": true, "cancelled": true, "skipped": true}
	var bTotal, bDone, bPct, pTotal, pDone, pPct int
	busyKeys := map[string]bool{}
	for _, j := range a.build.Jobs {
		rj := remoteJob{Name: j.Name, Kind: j.Kind, State: j.State, Pct: j.Pct, Note: j.Note}
		if j.Kind == "build" || j.Kind == "remove" {
			bTotal++
			switch {
			case fin[j.State] || j.State == "removing":
				bDone++
				bPct += 1000
			case j.State != "queued":
				bPct += j.Pct
				rs.Build.What = map[string]string{"verifying": "Checking ", "removing": "Removing "}[j.State]
				if rs.Build.What == "" {
					rs.Build.What = "Building "
				}
				rs.Build.What += j.Name
			}
			if !fin[j.State] {
				busyKeys[j.Key] = true
			}
		}
		if (j.Kind == "build" && j.Send) || j.Kind == "send" || j.Kind == "ps5del" || j.Kind == "ps5move" {
			pTotal++
			s := ps5JobState(j)
			pct := j.Pct
			if j.Kind == "build" {
				pct = j.SendPct
			}
			switch {
			case fin[s]:
				pDone++
				pPct += 1000
			case s == "sending" || s == "deleting" || s == "moving":
				pPct += pct
				verb := map[string]string{"deleting": "Deleting ", "moving": "Moving "}[s]
				if verb == "" {
					verb = "Sending "
				}
				rs.PS5.What = verb + j.Name
			}
			if !fin[s] {
				busyKeys[j.Key] = true
			}
			if j.Kind == "build" {
				rj.Note = strings.TrimSpace(rj.Note + " " + map[string]string{"waiting": "(will send)", "sending": fmt.Sprintf("(sending %d%%)", j.SendPct/10), "sent": "(sent)", "failed": "(not sent: " + j.SendNote + ")"}[j.SendState])
			}
		}
		rs.Jobs = append(rs.Jobs, rj)
	}
	rs.Build.Busy, rs.Build.Speed, rs.Build.ETA, rs.Build.Done, rs.Build.Total = a.build.Building, a.build.Speed, a.build.ETA, bDone, bTotal
	rs.PS5.Busy, rs.PS5.Speed, rs.PS5.ETA, rs.PS5.Done, rs.PS5.Total = a.build.PS5Busy, a.build.SendSpeed, a.build.SendETA, pDone, pTotal
	if bTotal > 0 {
		rs.Build.Pct = bPct / bTotal
	}
	if pTotal > 0 {
		rs.PS5.Pct = pPct / pTotal
	}
	// the phone always gets a drive to pick, even when the PS5 hasn't been
	// looked at yet: fall back to the usual places
	if rs.HasPS5 && len(rs.Drives) == 0 {
		for _, root := range []string{"/data", "/mnt/usb0", "/mnt/usb1", "/mnt/ext1"} {
			label, short, kind := driveLabel(root)
			rs.Drives = append(rs.Drives, PS5Drive{Root: root, Label: label, Short: short, Kind: kind, Free: -1, Total: -1, Dir: a.dirForLocked(root)})
		}
	}
	if rs.Drive == "" {
		rs.Drive = rootOf(a.cfg.PS5Dir)
	}
	for _, it := range st.Items {
		if it.IsDLC || it.IsFile || !(it.Present || it.ExfatOnly) {
			continue
		}
		g := remoteGame{Key: it.Key, Name: it.Name, ID: it.TitleID, Size: it.Size, HasImage: it.Image != "", CanBuild: it.CanBuild, OnPS5: it.OnPS5, Busy: busyKeys[it.Key]}
		if g.Name == "" {
			g.Name = it.Current
		}
		if it.Cover {
			k := it.CoverKey
			if k == "" {
				k = it.LibKey
			}
			g.Cover = k
		}
		rs.Games = append(rs.Games, g)
	}
	sort.Slice(rs.Games, func(i, j int) bool { return strings.ToLower(rs.Games[i].Name) < strings.ToLower(rs.Games[j].Name) })
	return rs
}

func (a *App) remoteHandler(rs *remoteServer) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	authed := func(r *http.Request) bool {
		c, err := r.Cookie("tt_remote")
		return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(rs.token)) == 1
	}
	guard := func(fn func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authed(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Enter your PIN.", "login": true})
				return
			}
			fn(w, r)
		}
	}
	post := func(fn func(r *http.Request) (any, error)) http.HandlerFunc {
		return guard(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get("X-Loadout") != "1" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
				return
			}
			d, err := fn(r)
			if err != nil {
				writeJSON(w, 200, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "data": d})
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := uiFiles.ReadFile("ui/remote.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write(b)
	})
	mux.HandleFunc("/r/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 400, map[string]any{"error": "bad request"})
			return
		}
		var b struct{ PIN string }
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b)
		rs.mu.Lock()
		if time.Now().Before(rs.lockedTo) {
			left := time.Until(rs.lockedTo).Round(time.Second)
			rs.mu.Unlock()
			writeJSON(w, 429, map[string]any{"error": fmt.Sprintf("Too many wrong PINs. Try again in %s.", fmtDur(left))})
			return
		}
		a.mu.Lock()
		pin := a.cfg.RemotePIN
		a.mu.Unlock()
		if len(pin) < 4 || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(b.PIN)), []byte(pin)) != 1 {
			rs.fails++
			msg := "Wrong PIN."
			if rs.fails >= 5 {
				rs.fails = 0
				rs.lockedTo = time.Now().Add(5 * time.Minute)
				msg = "Too many wrong PINs. The page is locked for 5 minutes."
			}
			rs.mu.Unlock()
			time.Sleep(600 * time.Millisecond)
			writeJSON(w, 401, map[string]any{"error": msg})
			return
		}
		rs.fails = 0
		rs.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "tt_remote", Value: rs.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("/r/state", guard(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		st := a.remoteStateLocked()
		a.mu.Unlock()
		writeJSON(w, 200, st)
	}))
	mux.HandleFunc("/r/cover", guard(func(w http.ResponseWriter, r *http.Request) {
		k := r.URL.Query().Get("k")
		a.mu.Lock()
		var p string
		if e := a.lib.Games[k]; e != nil {
			p = e.Thumb
			if p == "" || !fileExists(p) {
				p = e.Icon // the small copy is missing: use the full cover
			}
		}
		a.mu.Unlock()
		if id := strings.TrimPrefix(k, "id:"); id != k && gameID(id) == id {
			p = ps5IconPath(id)
		}
		b, err := os.ReadFile(p)
		if p == "" || err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Write(b)
	}))
	type startReq struct {
		Keys  []string
		Send  bool
		Drive string
	}
	mux.HandleFunc("/r/build", post(func(r *http.Request) (any, error) {
		var b startReq
		json.NewDecoder(r.Body).Decode(&b)
		var send []string
		dests := map[string]string{}
		if b.Send {
			send = b.Keys
			for _, k := range b.Keys {
				dests[k] = b.Drive
			}
		}
		return nil, a.startBuild(b.Keys, send, dests, false)
	}))
	mux.HandleFunc("/r/send", post(func(r *http.Request) (any, error) {
		var b startReq
		json.NewDecoder(r.Body).Decode(&b)
		a.mu.Lock()
		st := a.stateLocked()
		a.toast = append(st.Toasts, a.toast...)
		a.mu.Unlock()
		var items []sendItem
		for _, k := range b.Keys {
			for _, it := range st.Items {
				if it.Key == k && it.Image != "" {
					n := it.Name
					if n == "" {
						n = it.Current
					}
					items = append(items, sendItem{Key: k, Name: n, Image: it.Image, Drive: b.Drive})
				}
			}
		}
		if len(items) == 0 {
			return nil, errors.New("That game doesn't have an image yet. Build it first.")
		}
		return nil, a.startSend(items)
	}))
	mux.HandleFunc("/r/payload", post(func(r *http.Request) (any, error) {
		var b struct {
			I    int    `json:"i"`
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		a.mu.Lock()
		var path string
		var port int
		if b.I >= 0 && b.I < len(a.cfg.Payloads) && filepath.Base(a.cfg.Payloads[b.I].Path) == b.Name {
			path, port = a.cfg.Payloads[b.I].Path, a.cfg.Payloads[b.I].Port
		}
		a.mu.Unlock()
		if path == "" {
			return nil, errors.New("That payload isn't in Loadout's list any more. Refresh and try again.")
		}
		msg, err := a.runPayload(path, port)
		if err != nil {
			return nil, err
		}
		return map[string]string{"msg": msg}, nil
	}))
	mux.HandleFunc("/r/stop", post(func(r *http.Request) (any, error) {
		var b struct{ Lane string }
		json.NewDecoder(r.Body).Decode(&b)
		a.stopLane(b.Lane)
		return nil, nil
	}))
	return mux
}

// ---------- alerts ----------

var alertHTTP = &http.Client{Timeout: 15 * time.Second}

// sendAlert pushes a note to the phone through ntfy and/or a Discord webhook.
func sendAlert(ntfy, discord, title, body string, bad bool) error {
	var errs []string
	if ntfy = strings.TrimSpace(ntfy); ntfy != "" {
		u := ntfy
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			u = "https://ntfy.sh/" + strings.TrimPrefix(u, "/")
		}
		req, err := http.NewRequest("POST", u, strings.NewReader(body))
		if err == nil {
			req.Header.Set("Title", title)
			tag := "white_check_mark"
			if bad {
				tag = "warning"
				req.Header.Set("Priority", "high")
			}
			req.Header.Set("Tags", tag)
			resp, err2 := alertHTTP.Do(req)
			err = err2
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 300 {
					err = fmt.Errorf("ntfy said %s", resp.Status)
				}
			}
		}
		if err != nil {
			errs = append(errs, "ntfy: "+err.Error())
		}
	}
	if discord = strings.TrimSpace(discord); discord != "" {
		if !strings.HasPrefix(discord, "https://discord.com/api/webhooks/") && !strings.HasPrefix(discord, "https://discordapp.com/api/webhooks/") {
			errs = append(errs, "Discord: that isn't a Discord webhook link")
		} else {
			payload, _ := json.Marshal(map[string]string{"username": "Loadout", "content": "**" + title + "**\n" + body})
			resp, err := alertHTTP.Post(discord, "application/json", bytes.NewReader(payload))
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 300 {
					err = fmt.Errorf("Discord said %s", resp.Status)
				}
			}
			if err != nil {
				errs = append(errs, "Discord: "+err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// alertRunLocked tells the phone a run finished (called with a.mu held).
func (a *App) alertRunLocked(summary string) {
	ntfy, discord := a.cfg.AlertNtfy, a.cfg.AlertDiscord
	if strings.TrimSpace(ntfy) == "" && strings.TrimSpace(discord) == "" {
		return
	}
	var failed []string
	for _, j := range a.build.Jobs {
		if j.State == "failed" {
			failed = append(failed, j.Name+": "+j.Note)
		} else if j.SendState == "failed" {
			failed = append(failed, j.Name+" (not sent): "+j.SendNote)
		}
	}
	title, body, bad := "Loadout: all done", summary, false
	if len(failed) > 0 {
		title, bad = "Loadout: finished with problems", true
		if len(failed) > 6 {
			failed = append(failed[:6], "…")
		}
		body += "\n" + strings.Join(failed, "\n")
	} else if strings.Contains(summary, "stopped") {
		title = "Loadout: stopped"
	}
	go sendAlert(ntfy, discord, title, body, bad)
}

// stopLane cancels the build lane, the PS5 lane, or both ("").
func (a *App) stopLane(lane string) {
	if lane != "ps5" {
		atomic.StoreInt32(&a.cancel, 1)
	}
	if lane != "build" {
		atomic.AddInt32(&a.ps5stop, 1)
		// games still being built won't be sent either
		go func() {
			a.mu.Lock()
			for _, j := range a.build.Jobs {
				if j.Kind == "build" && j.Send && j.SendState == "waiting" {
					j.SendState, j.SendNote = "cancelled", "Stopped"
				}
				// show right away that Stop was heard
				if j.Kind == "build" && j.SendState == "sending" {
					j.SendNote = "Stopping"
				} else if j.Kind != "build" && j.State == "sending" {
					j.Note = "Stopping"
				}
			}
			a.mu.Unlock()
		}()
	}
}
