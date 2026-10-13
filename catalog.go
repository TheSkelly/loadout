package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The store: what the Loadout app on the PS5 talks to. It runs on the phone
// page's server (same port, same PIN) under /s/. The PS5 app pairs once with
// the PIN and gets a device token it keeps, so it doesn't need cookies and
// keeps working after Loadout restarts. Like the phone page, the store can
// only look and ask for games: it can't rename, delete or change settings.

// StoreDevice is a paired PS5 (or anything else that paired with the PIN).
type StoreDevice struct {
	ID    string    // short id shown in Settings
	Name  string    // what the device called itself
	Hash  string    // sha256 of its token; the token itself is never saved
	Added time.Time
	Seen  time.Time
}

// StoreDeviceView is a paired device as Settings shows it.
type StoreDeviceView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Added string `json:"added"`
	Seen  string `json:"seen"`
}

const maxStoreDevices = 8

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (a *App) storeDeviceViewsLocked() []StoreDeviceView {
	res := []StoreDeviceView{}
	for _, d := range a.cfg.StoreDevices {
		v := StoreDeviceView{ID: d.ID, Name: d.Name, Added: d.Added.Format("Jan 2, 2006")}
		if !d.Seen.IsZero() {
			v.Seen = d.Seen.Format("Jan 2, 3:04 PM")
		}
		res = append(res, v)
	}
	return res
}

// storeDeviceLocked finds the paired device a token belongs to.
func (a *App) storeDeviceLocked(token string) *StoreDevice {
	if len(token) < 32 {
		return nil
	}
	h := tokenHash(token)
	for i := range a.cfg.StoreDevices {
		if subtle.ConstantTimeCompare([]byte(a.cfg.StoreDevices[i].Hash), []byte(h)) == 1 {
			return &a.cfg.StoreDevices[i]
		}
	}
	return nil
}

// pairStoreDevice remembers a new device and returns the token it must keep.
func (a *App) pairStoreDevice(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "PS5"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	token := randHex(32)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.cfg.StoreDevices = append(a.cfg.StoreDevices, StoreDevice{ID: randHex(3), Name: name, Hash: tokenHash(token), Added: now, Seen: now})
	if n := len(a.cfg.StoreDevices); n > maxStoreDevices {
		a.cfg.StoreDevices = a.cfg.StoreDevices[n-maxStoreDevices:] // the oldest pairing drops off
	}
	saveJSON("settings.json", a.cfg)
	return token
}

func (a *App) forgetStoreDeviceLocked(id string) {
	kept := a.cfg.StoreDevices[:0]
	for _, d := range a.cfg.StoreDevices {
		if d.ID != id {
			kept = append(kept, d)
		}
	}
	a.cfg.StoreDevices = kept
}

// ---------- what the store shows ----------

type storeGame struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	ID        string   `json:"id"`
	Platform  string   `json:"platform"`
	Version   string   `json:"version"`
	Size      int64    `json:"size"`
	Publisher string   `json:"publisher"`
	Release   string   `json:"release"`
	Genres    []string `json:"genres"`
	Desc      string   `json:"desc"`
	Rating    float64  `json:"rating"`
	Ratings   int      `json:"ratings"`
	Rated     string   `json:"rated"`
	Cover     string   `json:"cover"` // art key for /s/art, "" when there is none
	Hero      string   `json:"hero"`
	Shots     int      `json:"shots"`
	Ready     bool     `json:"ready"`  // an image is built, so it can be sent right away
	CanGet    bool     `json:"canGet"` // it can be built (if needed) and sent
	OnPS5     string   `json:"onPs5"`  // where it already is on the PS5, if anywhere
	FirstSeen string   `json:"firstSeen"`
	storeProgress
}

type storeProgress struct {
	State string `json:"state"` // "", queued, building, waiting, sending, sent, failed
	Pct   int    `json:"pct"`   // 0..1000 for the current step
	Note  string `json:"note"`
}

type storeStatus struct {
	Version string                   `json:"version"`
	PC      string                   `json:"pc"`
	Online  bool                     `json:"ps5Online"`
	HasPS5  bool                     `json:"hasPs5"`
	Build   remoteLane               `json:"build"`
	PS5     remoteLane               `json:"ps5"`
	Games   map[string]storeProgress `json:"games"` // only games with something going on
	Drives  []PS5Drive               `json:"drives"`
	Drive   string                   `json:"drive"`
}

// storeProgressLocked is where each game is in the build and send queues.
func (a *App) storeProgressLocked() map[string]storeProgress {
	res := map[string]storeProgress{}
	for _, j := range a.build.Jobs {
		var p storeProgress
		switch {
		case j.Kind == "build" && j.Send:
			switch j.State {
			case "queued":
				p = storeProgress{State: "queued"}
			case "building", "verifying":
				p = storeProgress{State: "building", Pct: j.Pct}
			case "done":
				p = storeProgress{State: j.SendState, Pct: j.SendPct, Note: j.SendNote}
			default:
				p = storeProgress{State: "failed", Note: j.Note}
			}
		case j.Kind == "send":
			p = storeProgress{State: j.State, Pct: j.Pct, Note: j.Note}
			if p.State == "done" {
				p.State = "sent"
			}
		default:
			continue
		}
		if p.State == "cancelled" || p.State == "skipped" {
			p.State = ""
		}
		if p.State != "" {
			res[j.Key] = p // later jobs for the same game win
		}
	}
	return res
}

func (a *App) storeGamesLocked() []storeGame {
	st := a.stateLocked()
	a.toast = append(st.Toasts, a.toast...) // reading for the PS5 must not swallow the desktop's toasts
	prog := a.storeProgressLocked()
	games := []storeGame{}
	for _, it := range st.Items {
		if it.IsDLC || it.IsFile || !(it.Present || it.ExfatOnly) {
			continue
		}
		g := storeGame{Key: it.Key, Name: it.Name, ID: it.TitleID, Platform: it.Platform, Version: it.Version, Size: it.Size,
			Publisher: it.Publisher, Release: it.Release, Genres: it.Genres, Desc: it.Desc, Rating: it.Rating, Ratings: it.Ratings,
			Rated: it.Rated, Shots: it.Shots, Ready: it.Image != "", CanGet: it.Image != "" || it.CanBuild, OnPS5: it.OnPS5,
			FirstSeen: it.FirstSeen, storeProgress: prog[it.Key]}
		if g.Name == "" {
			g.Name = it.Current
		}
		if g.Genres == nil {
			g.Genres = []string{}
		}
		if it.Cover {
			g.Cover = it.CoverKey
			if g.Cover == "" {
				g.Cover = it.LibKey
			}
		}
		if it.Hero {
			g.Hero = it.LibKey
		}
		games = append(games, g)
	}
	sort.Slice(games, func(i, j int) bool { return strings.ToLower(games[i].Name) < strings.ToLower(games[j].Name) })
	return games
}

func (a *App) storeStatusLocked() storeStatus {
	rs := a.remoteStateLocked()
	// on the PS5 a send is a download, and building is the PC getting it ready
	rs.PS5.What = strings.Replace(rs.PS5.What, "Sending ", "Downloading ", 1)
	rs.Build.What = strings.Replace(rs.Build.What, "Building ", "Preparing ", 1)
	return storeStatus{Version: rs.Version, PC: rs.PC, Online: rs.Online, HasPS5: rs.HasPS5, Build: rs.Build, PS5: rs.PS5,
		Games: a.storeProgressLocked(), Drives: rs.Drives, Drive: rs.Drive}
}

// storeGet builds (when needed) and sends one game to the PS5. Its messages
// are shown on the PS5, where this is called downloading.
func (a *App) storeGet(key, drive string) (string, error) {
	a.mu.Lock()
	st := a.stateLocked()
	a.toast = append(st.Toasts, a.toast...)
	busy := a.storeProgressLocked()[key]
	building := a.build.Building
	a.mu.Unlock()
	if s := busy.State; s != "" && s != "sent" && s != "failed" {
		return "", errors.New("That game is already downloading.")
	}
	for _, it := range st.Items {
		if it.Key != key || it.IsDLC || it.IsFile {
			continue
		}
		name := it.Name
		if name == "" {
			name = it.Current
		}
		if it.Image != "" {
			if err := a.startSend([]sendItem{{Key: key, Name: name, Image: it.Image, Drive: drive}}); err != nil {
				return "", err
			}
			return "Downloading " + name + ".", nil
		}
		if !it.CanBuild {
			return "", errors.New("Your PC can't prepare that game yet. Check it in Loadout on the PC.")
		}
		if building {
			return "", errors.New("Your PC is preparing another game right now. Try again when it's done.")
		}
		if err := a.startBuild([]string{key}, []string{key}, map[string]string{key: drive}, false); err != nil {
			return "", err
		}
		return "Your PC is preparing " + name + ". The download starts when it's ready.", nil
	}
	return "", errors.New("That game isn't in Loadout's library any more.")
}

// storeArtPath finds the picture behind an art key: "cover" is the game's
// icon, "hero" its big background.
func (a *App) storeArtPath(k, f string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.lib.Games[k]
	switch f {
	case "hero":
		if e != nil && e.Hero != "" && strings.HasPrefix(filepath.Clean(e.Hero), filepath.Clean(artDir())) {
			return e.Hero
		}
		return ""
	case "full":
		if e != nil {
			return e.Icon
		}
	}
	if id := strings.TrimPrefix(k, "id:"); id != k && gameID(id) == id {
		return ps5IconPath(id)
	}
	if e == nil {
		return ""
	}
	if e.Thumb != "" && fileExists(e.Thumb) {
		return e.Thumb
	}
	return e.Icon
}

// ---------- routes ----------

// storeRoutes adds the store to the phone page's server.
func (a *App) storeRoutes(mux *http.ServeMux, rs *remoteServer, writeJSON func(http.ResponseWriter, int, any), cookieOK func(*http.Request) bool) {
	authed := func(r *http.Request) bool {
		if cookieOK(r) {
			return true // the store page opened from a browser that's logged in to the phone page
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		d := a.storeDeviceLocked(r.Header.Get("X-Loadout-Token"))
		if d == nil {
			return false
		}
		if time.Since(d.Seen) > time.Hour {
			d.Seen = time.Now()
			saveJSON("settings.json", a.cfg)
		}
		return true
	}
	get := func(fn func(r *http.Request) any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authed(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Pair with Loadout first.", "pair": true})
				return
			}
			writeJSON(w, 200, fn(r))
		}
	}
	post := func(fn func(r *http.Request) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
				return
			}
			if !authed(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Pair with Loadout first.", "pair": true})
				return
			}
			d, err := fn(r)
			if err != nil {
				writeJSON(w, 200, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "data": d})
		}
	}

	// hello answers without a token so the PS5 app can tell it found Loadout
	mux.HandleFunc("/s/hello", func(w http.ResponseWriter, r *http.Request) {
		host, _ := os.Hostname()
		writeJSON(w, 200, map[string]any{"app": "Loadout", "version": appVersion, "pc": host, "paired": authed(r)})
	})
	mux.HandleFunc("/s/pair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 400, map[string]any{"error": "bad request"})
			return
		}
		var b struct{ PIN, Name string }
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b)
		if code, msg := a.checkRemotePIN(rs, b.PIN); code != 200 {
			writeJSON(w, code, map[string]any{"error": msg})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "token": a.pairStoreDevice(b.Name)})
	})
	mux.HandleFunc("/s/games", get(func(r *http.Request) any {
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string]any{"games": a.storeGamesLocked()}
	}))
	mux.HandleFunc("/s/status", get(func(r *http.Request) any {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.storeStatusLocked()
	}))
	mux.HandleFunc("/s/art", func(w http.ResponseWriter, r *http.Request) {
		// picture tags can't send headers, so art also takes the token as ?t=
		if t := r.URL.Query().Get("t"); t != "" && r.Header.Get("X-Loadout-Token") == "" {
			r.Header.Set("X-Loadout-Token", t)
		}
		if !authed(r) {
			http.Error(w, "pair first", http.StatusUnauthorized)
			return
		}
		p := a.storeArtPath(r.URL.Query().Get("k"), r.URL.Query().Get("f"))
		b, err := os.ReadFile(p)
		if p == "" || err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Write(b)
	})
	mux.HandleFunc("/s/get", post(func(r *http.Request) (any, error) {
		var b struct{ Key, Drive string }
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b)
		msg, err := a.storeGet(b.Key, b.Drive)
		return map[string]string{"msg": msg}, err
	}))
	mux.HandleFunc("/s/stop", post(func(r *http.Request) (any, error) {
		var b struct{ Lane string }
		json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&b)
		a.stopLane(b.Lane)
		return nil, nil
	}))
	mux.HandleFunc("/store", func(w http.ResponseWriter, r *http.Request) {
		b, _ := uiFiles.ReadFile("ui/store.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write(b)
	})
}

// ---------- finding Loadout on the network ----------

const discoverAsk = "LOADOUT_DISCOVER"

// serveDiscovery answers the PS5 app's broadcast so it can find this PC
// without anyone typing an IP address. It listens on the same port number
// as the phone page, over UDP, until conn is closed.
func serveDiscovery(conn net.PacketConn, port int) {
	host, _ := os.Hostname()
	reply, _ := json.Marshal(map[string]any{"app": "Loadout", "version": appVersion, "pc": host, "port": port})
	buf := make([]byte, 256)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if strings.TrimSpace(string(buf[:n])) == discoverAsk {
			conn.WriteTo(reply, from)
		}
	}
}
