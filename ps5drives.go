package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PS5Drive is one place on the PS5 that games can be sent to.
type PS5Drive struct {
	Root  string `json:"root"`  // "/data" (internal), "/mnt/usb0", "/mnt/ext1"...
	Label string `json:"label"` // "Internal SSD", "USB drive (usb0)"
	Short string `json:"short"` // "Internal", "USB 0"
	Kind  string `json:"kind"`  // internal, usb, ext
	Free  int64  `json:"free"`  // bytes, -1 when unknown
	Total int64  `json:"total"` // bytes, -1 when unknown
	Slack int64  `json:"-"`     // rounding error in Free (df prints 1 decimal)
	Dir   string `json:"dir"`   // folder games go to on this drive
	Empty bool   `json:"empty"` // the mount point is empty, so it may not be plugged in
}

// PS5Info is what Loadout last learned about the PS5's drives.
type PS5Info struct {
	Report    []string   `json:"report"`  // what the last scan saw in each folder
	Partial   bool       `json:"partial"` // the last scan couldn't read every folder
	Online    bool       `json:"online"`
	Shell     bool       `json:"shell"`     // free space is known (shsrv's df or PS5Upload)
	SpaceFrom string     `json:"spaceFrom"` // "shsrv" or "PS5Upload"
	SpaceErr  string     `json:"spaceErr"`  // why free space is unknown
	Drives    []PS5Drive `json:"drives"`
	Checked   time.Time  `json:"checked"`
	Err       string     `json:"err"`
	Busy      bool       `json:"busy"`
	// Games maps a title ID to every copy found on the PS5
	Games   map[string][]PS5Copy `json:"games"`
	Scanned bool                 `json:"scanned"`
	Icons   map[string]bool      `json:"icons"` // title IDs we have a PS5 icon for
}

// ps5IconPath is where an icon pulled from the PS5 is kept on the PC.
func ps5IconPath(id string) string {
	return filepath.Join(appDir(), "ps5icons", id+".png")
}

// fetchIcon copies an icon0.png from the PS5 once; later scans reuse it.
func fetchIcon(f *ftpConn, id string, candidates ...string) {
	dst := ps5IconPath(id)
	if fileExists(dst) {
		return
	}
	for _, c := range candidates {
		b, err := f.get(c, 8<<20)
		if err != nil || len(b) < 8 || !strings.HasPrefix(http.DetectContentType(b), "image/") {
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0755)
		if os.WriteFile(dst+".tmp", b, 0644) == nil {
			os.Rename(dst+".tmp", dst)
		}
		return
	}
}

// PS5Copy is one copy of a game found on the PS5.
type PS5Copy struct {
	Drive string `json:"drive"` // "Internal", "Ext 1"
	Path  string `json:"path"`
	Kind  string `json:"kind"` // image, folder, installed
	Size  int64  `json:"size"`
	Name  string `json:"name"` // title from the PS5's own records, for installed games
}

// gameID only accepts real game ID prefixes (PPSA, CUSA...), so homebrew
// apps like FMGR88888 or FAKE10101 aren't mistaken for games.
func gameID(s string) string {
	if m := knownIDRe.FindStringSubmatch(strings.ToUpper(s)); m != nil {
		return m[1] + m[2]
	}
	return ""
}

// appMeta lists what the PS5 itself considers installed (its app metadata
// folders) and reads titles for them, caching names between scans.
func appMeta(f *ftpConn, names map[string]string) (map[string]bool, bool) {
	ids := map[string]bool{}
	known := false
	for _, dir := range []string{"/system_data/priv/appmeta", "/user/appmeta"} {
		ents, err := f.list(dir)
		if err != nil {
			continue
		}
		known = true
		for _, e := range ents {
			id := gameID(e.Name)
			if !e.IsDir || id == "" || ids[id] {
				continue
			}
			ids[id] = true
			if !fileExists(ps5IconPath(id)) {
				cands := []string{path.Join(dir, e.Name, "icon0.png")}
				if inside, err := f.list(path.Join(dir, e.Name)); err == nil {
					for _, x := range inside {
						n := strings.ToLower(x.Name)
						if !x.IsDir && strings.HasPrefix(n, "icon0") && strings.HasSuffix(n, ".png") && n != "icon0.png" {
							cands = append(cands, path.Join(dir, e.Name, x.Name))
						}
					}
				}
				fetchIcon(f, id, cands...)
			}
			if names[id] != "" {
				continue
			}
			tmp, err := os.MkdirTemp("", "ttmeta")
			if err != nil {
				continue
			}
			for _, fn := range []string{"param.json", "param.sfo"} {
				b, err := f.get(path.Join(dir, e.Name, fn), 4<<20)
				if err != nil || len(b) == 0 {
					continue
				}
				lp := filepath.Join(tmp, fn)
				os.WriteFile(lp, b, 0644)
				var m Meta
				if fn == "param.json" {
					m, err = readParamJSON(lp)
				} else {
					m, err = readParamSFO(lp)
				}
				if err == nil && m.Name != "" {
					names[id] = m.Name
					break
				}
			}
			os.RemoveAll(tmp)
		}
	}
	return ids, known
}

var gameFileExt = map[string]bool{".exfat": true, ".ffpkg": true, ".ffpfsc": true}

// scanPS5Games lists the places ShadowMount+ looks for games (plus installed
// apps) and returns every copy it can tie to a title ID.
// scanPS5Games lists the games on every drive. ok is false when the PS5
// failed to answer for a folder, so the result may be missing games.
func scanPS5Games(f *ftpConn, drives []PS5Drive, names map[string]string, report *[]string) (res map[string][]PS5Copy, ok bool) {
	note := func(format string, a ...any) {
		if report != nil {
			*report = append(*report, fmt.Sprintf(format, a...))
		}
	}
	var feats []string
	for k := range f.feat {
		feats = append(feats, k)
	}
	sort.Strings(feats)
	note("FTP server says: %q, supports: %s", f.banner, strings.Join(feats, " "))
	res = map[string][]PS5Copy{}
	ok = true
	seen := map[string]bool{}
	installed, metaKnown := appMeta(f, names)
	look := func(dir, drive, kind string) {
		dir = path.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		ents, err := f.list(dir)
		if err != nil {
			if !strings.Contains(err.Error(), "isn't on the PS5") {
				ok = false // not a missing folder: the PS5 didn't answer properly
			}
			note("%s: %v", dir, err)
			return
		}
		found := 0
		var others []string
		defer func() {
			line := fmt.Sprintf("%s: %d item(s), %d game(s)", dir, len(ents), found)
			if len(others) > 0 {
				if len(others) > 6 {
					others = append(others[:6], "…")
				}
				line += ". Not counted: " + strings.Join(others, ", ")
			}
			if len(ents) == 0 && strings.TrimSpace(f.lastRaw) != "" {
				line += fmt.Sprintf(". The PS5 sent a listing Loadout couldn't read: %q", f.lastRaw)
			}
			note("%s", line)
		}()
		for _, e := range ents {
			name := e.Name
			others = append(others, name)
			if strings.HasPrefix(name, ".") || strings.HasSuffix(strings.ToLower(name), ".part") {
				continue
			}
			k := kind
			if kind == "installed" {
				// The PS5 keeps a game's folder and its records after the game is
				// deleted, and after an image it was mounted from is removed. So
				// only count it when the game's files are really in the folder.
				if metaKnown && !installed[gameID(name)] {
					continue
				}
				inside, err := f.list(path.Join(dir, name))
				if err != nil {
					continue
				}
				real := false
				for _, x := range inside {
					n := strings.ToLower(x.Name)
					// an installed game has its package or its program here. A
					// folder with only artwork (sce_sys) is what's left behind
					// when a mounted image is removed.
					if (n == "app.pkg" && x.Size > 1<<20) || n == "eboot.bin" {
						real = true
					}
				}
				if !real {
					var names []string
					for _, x := range inside {
						names = append(names, x.Name)
					}
					note("%s: leftover folder, not counted as installed (holds: %s)", path.Join(dir, name), strings.Join(names, ", "))
					continue
				}
			}
			if !e.IsDir {
				if !gameFileExt[strings.ToLower(path.Ext(name))] {
					continue
				}
				k = "image"
			} else if k == "" {
				k = "folder"
			}
			id := gameID(name)
			if id == "" {
				continue // homebrew and other non-game apps
			}
			if _, emu := knownEmus[id]; emu && e.IsDir {
				continue // an emulator: it has its own tab
			}
			if id == ps5AppID {
				continue // Loadout's own PS5 app, not a game
			}
			found++
			others = others[:len(others)-1]
			c := PS5Copy{Drive: drive, Path: path.Join(dir, name), Kind: k, Size: e.Size}
			if k == "folder" {
				// game folders carry their own icon, either at the top or in the -app folder
				fetchIcon(f, id, path.Join(c.Path, "sce_sys", "icon0.png"), path.Join(c.Path, id+"-app", "sce_sys", "icon0.png"))
			}
			if k == "installed" {
				c.Name = names[id]
			}
			res[id] = append(res[id], c)
		}
	}
	for _, d := range drives {
		if d.Kind == "internal" {
			look("/data/etaHEN/games", d.Short, "")
			look("/data/homebrew", d.Short, "")
			look("/user/app", d.Short, "installed")
		} else {
			look(d.Root, d.Short, "")
			look(d.Root+"/etaHEN/games", d.Short, "")
			look(d.Root+"/homebrew", d.Short, "")
		}
		if d.Dir != "" {
			look(d.Dir, d.Short, "")
		}
	}
	// A game sent as an image or folder also shows up as "installed" once the
	// PS5 mounts it. That's the same game, so keep only the copy you sent.
	for id, cs := range res {
		real := cs[:0:0]
		for _, c := range cs {
			if c.Kind != "installed" {
				real = append(real, c)
			}
		}
		if len(real) > 0 && len(real) < len(cs) {
			res[id] = real
		}
	}
	return res, ok
}

const shellPort = 2323 // ps5-payload-dev/shsrv

// placeholderMax: anything smaller mounted on /mnt/usbN or /mnt/extN is the
// PS5's empty-slot placeholder, not a drive.
const placeholderMax = 2 << 30

var driveRe = regexp.MustCompile(`^/mnt/(usb|ext)(\d+)`)

// rootOf says which drive a PS5 path lives on.
func rootOf(p string) string {
	p = path.Clean("/" + strings.TrimSpace(p))
	if m := driveRe.FindString(p); m != "" {
		return m
	}
	return "/data"
}

// relOf is the part of p below its drive root ("etaHEN/games").
func relOf(p string) string {
	p = path.Clean("/" + strings.TrimSpace(p))
	r := rootOf(p)
	if r != "/data" {
		return strings.TrimPrefix(strings.TrimPrefix(p, r), "/")
	}
	for _, pre := range []string{"/user/data", "/data", "/user"} {
		if p == pre {
			return ""
		}
		if strings.HasPrefix(p, pre+"/") {
			return strings.TrimPrefix(p, pre+"/")
		}
	}
	return strings.TrimPrefix(p, "/")
}

func driveLabel(root string) (label, short, kind string) {
	if root == "/data" {
		return "Internal SSD", "Internal", "internal"
	}
	m := driveRe.FindStringSubmatch(root)
	if m == nil {
		return root, root, "other"
	}
	if m[1] == "usb" {
		return "USB drive (usb" + m[2] + ")", "USB " + m[2], "usb"
	}
	return "Extended storage (ext" + m[2] + ")", "Ext " + m[2], "ext"
}

// dfEntry is one line of shsrv's df output.
type dfEntry struct {
	Mount        string
	Total, Avail int64
	Slack        int64
}

var dfSize = regexp.MustCompile(`^([\d.]+)([BKMGT])$`)

func parseHuman(s string) (int64, int64, bool) {
	m := dfSize.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, 0, false
	}
	unit := map[string]float64{"B": 1, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}[m[2]]
	return int64(v * unit), int64(unit * 0.05), true
}

// parseDF reads shsrv's df table:
// Filesystem  Size  Used  Avail  Capacity  Mounted on
func parseDF(out string) []dfEntry {
	var res []dfEntry
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimRight(ln, "\r"))
		if len(f) < 6 || !strings.HasSuffix(f[len(f)-2], "%") {
			continue
		}
		tot, _, ok1 := parseHuman(f[len(f)-5])
		av, slack, ok2 := parseHuman(f[len(f)-3])
		if !ok1 || !ok2 {
			continue
		}
		res = append(res, dfEntry{Mount: f[len(f)-1], Total: tot, Avail: av, Slack: slack})
	}
	return res
}

// dfFor finds the mount that holds p (longest matching mount point).
func dfFor(ents []dfEntry, p string) (dfEntry, bool) {
	cands := []string{p}
	if rootOf(p) == "/data" {
		cands = append(cands, "/user/data"+strings.TrimPrefix(p, "/data"), "/user")
	}
	var best dfEntry
	found := false
	for _, c := range cands {
		for _, e := range ents {
			if e.Mount == "/" {
				continue // the system partition is never where games go
			}
			// ">=" so when two things are mounted on the same folder, the later
			// (top) one wins: that's the drive you actually see there
			if (c == e.Mount || strings.HasPrefix(c, strings.TrimSuffix(e.Mount, "/")+"/")) && len(e.Mount) >= len(best.Mount) {
				best, found = e, true
			}
		}
		if found {
			return best, true
		}
	}
	return best, false
}

// shellDF asks shsrv (a small telnet shell payload) for "df".
func shellDF(host string, timeout time.Duration) ([]dfEntry, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(shellPort)), 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	r := bufio.NewReader(c)
	var text strings.Builder
	// readUntil collects text, answering telnet option requests with "no".
	readUntil := func(done func(string) bool) error {
		for !done(text.String()) {
			b, err := r.ReadByte()
			if err != nil {
				return err
			}
			if b != 255 {
				text.WriteByte(b)
				continue
			}
			cmd, err := r.ReadByte()
			if err != nil {
				return err
			}
			switch cmd {
			case 255:
				text.WriteByte(255)
			case 251, 252, 253, 254: // WILL WONT DO DONT
				opt, err := r.ReadByte()
				if err != nil {
					return err
				}
				if cmd == 251 {
					c.Write([]byte{255, 254, opt})
				} else if cmd == 253 {
					c.Write([]byte{255, 252, opt})
				}
			case 250: // subnegotiation: skip to IAC SE
				for {
					x, err := r.ReadByte()
					if err != nil {
						return err
					}
					if x == 255 {
						if y, _ := r.ReadByte(); y == 240 {
							break
						}
					}
				}
			}
		}
		return nil
	}
	prompt := func(s string) bool { return strings.HasSuffix(strings.TrimRight(s, " "), "$") }
	if err := readUntil(prompt); err != nil {
		return nil, fmt.Errorf("the shell on port %d didn't answer", shellPort)
	}
	text.Reset()
	c.Write([]byte("df\r\n"))
	if err := readUntil(func(s string) bool { return strings.Contains(s, "Mounted on") && prompt(s) }); err != nil {
		return nil, errors.New("the shell didn't return disk usage")
	}
	c.Write([]byte("exit\r\n"))
	ents := parseDF(text.String())
	if len(ents) == 0 {
		return nil, errors.New("couldn't read the disk usage table")
	}
	return ents, nil
}

// probePS5 finds the drives on the PS5 and, when shsrv is running, their free space.
// appNames caches installed-game titles read from the PS5 between scans.
var (
	appNamesMu sync.Mutex
	appNames   = map[string]string{}
)

func probePS5(host string, port int, dirFor func(root string) string) PS5Info {
	appNamesMu.Lock()
	defer appNamesMu.Unlock()
	names := appNames
	info := PS5Info{Checked: time.Now()}
	f, err := ftpDial(host, port)
	if err != nil {
		info.Err = err.Error()
		return info
	}
	defer f.Close()
	info.Online = true
	roots := map[string]bool{"/data": true}
	empty := map[string]bool{}
	if ents, err := f.list("/mnt"); err == nil {
		for _, e := range ents {
			p := "/mnt/" + e.Name
			if e.IsDir && driveRe.MatchString(p) && rootOf(p) == p {
				inside, err := f.list(p)
				if err != nil {
					continue
				}
				roots[p] = true
				empty[p] = len(inside) == 0
			}
		}
	}
	df, src, dferr := diskUsage(host, 6*time.Second)
	if dferr != nil {
		info.SpaceErr = dferr.Error()
	}
	if dferr == nil {
		info.Shell, info.SpaceFrom = true, src
		// with df we know exactly what's mounted
		for _, e := range df {
			if driveRe.MatchString(e.Mount) && rootOf(e.Mount) == e.Mount {
				roots[e.Mount] = true
				empty[e.Mount] = false
			}
		}
		for r := range roots {
			if r != "/data" {
				// The PS5 keeps a tiny placeholder mounted on every USB and
				// extended-storage slot (about 1 MB), even with nothing plugged in.
				if e, ok := dfFor(df, r); !ok || e.Total < placeholderMax {
					delete(roots, r)
				}
			}
		}
	} else {
		// without df, an empty slot is most likely nothing plugged in
		for r := range roots {
			if r != "/data" && empty[r] {
				delete(roots, r)
			}
		}
	}
	for r := range roots {
		d := PS5Drive{Root: r, Free: -1, Total: -1, Dir: dirFor(r), Empty: empty[r]}
		d.Label, d.Short, d.Kind = driveLabel(r)
		if dferr == nil {
			if e, ok := dfFor(df, r); ok {
				d.Free, d.Total, d.Slack = e.Avail, e.Total, e.Slack
				d.Empty = false
			}
		}
		info.Drives = append(info.Drives, d)
	}
	order := map[string]int{"internal": 0, "ext": 1, "usb": 2}
	sort.Slice(info.Drives, func(i, j int) bool {
		a, b := info.Drives[i], info.Drives[j]
		if order[a.Kind] != order[b.Kind] {
			return order[a.Kind] < order[b.Kind]
		}
		return a.Root < b.Root
	})
	// external drives are simply Ext 1, Ext 2... in the header
	n := 0
	for i := range info.Drives {
		if info.Drives[i].Kind != "internal" {
			n++
			info.Drives[i].Short = "Ext " + strconv.Itoa(n)
		}
	}
	var scanOK bool
	// a fresh connection, so nothing earlier in this check can leave it out of step
	if g, err := ftpDial(host, port); err == nil {
		defer g.Close()
		f = g
	}
	info.Games, scanOK = scanPS5Games(f, info.Drives, names, &info.Report)
	info.Scanned, info.Partial = true, !scanOK
	info.Icons = map[string]bool{}
	for id := range info.Games {
		if fileExists(ps5IconPath(id)) {
			info.Icons[id] = true
		}
	}
	return info
}

// freeOn returns current free space for the drive holding dir, or -1.
func freeOn(host, dir string) (free, slack int64) {
	df, _, err := diskUsage(host, 5*time.Second)
	if err != nil {
		return -1, 0
	}
	if e, ok := dfFor(df, dir); ok {
		return e.Avail, e.Slack
	}
	return -1, 0
}

// ---------------- app side ----------------

// dirForLocked is the folder games go to on a drive.
func (a *App) dirForLocked(root string) string {
	if rootOf(a.cfg.PS5Dir) == root {
		return a.cfg.PS5Dir
	}
	if d := a.cfg.PS5Dirs[root]; d != "" && rootOf(d) == root {
		return d
	}
	rel := relOf(a.cfg.PS5Dir)
	if rel == "" {
		rel = "etaHEN/games"
	}
	return path.Join(root, rel)
}

func (a *App) refreshDirsLocked() {
	for i := range a.ps5info.Drives {
		a.ps5info.Drives[i].Dir = a.dirForLocked(a.ps5info.Drives[i].Root)
	}
}

// destLocked turns a drive root (or "" for the default) into a folder and a label.
func (a *App) destLocked(root string) (string, string) {
	if root == "" {
		root = rootOf(a.cfg.PS5Dir)
	}
	root = rootOf(root)
	label, _, _ := driveLabel(root)
	return a.dirForLocked(root), label
}

// refreshPS5 looks up the PS5's drives in the background.
func (a *App) refreshPS5() {
	a.mu.Lock()
	host, port, gen := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port, a.ps5gen
	if host == "" || (a.ps5info.Busy && time.Since(a.ps5busyAt) < 3*time.Minute) {
		a.mu.Unlock()
		return
	}
	a.ps5info.Busy, a.ps5busyAt = true, time.Now()
	p5uSpaceOK.Store(!a.cfg.PlainFTP)
	a.mu.Unlock()
	info := probePS5(host, port, func(r string) string {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.dirForLocked(r)
	})
	defer a.maybeAutoShell(info)
	a.mu.Lock()
	defer a.mu.Unlock()
	if gen != a.ps5gen {
		return // the address changed while we were looking
	}
	if !info.Online && len(a.ps5info.Drives) > 0 {
		// keep the last known drives so the pickers still work
		old := a.ps5info
		old.Online, old.Busy, old.Err, old.Checked = false, false, info.Err, info.Checked
		old.Games, old.Scanned = nil, false // can't vouch for what's on it any more
		a.ps5info = old
		return
	}
	if info.Partial && len(a.ps5info.Games) > 0 {
		// the PS5 was too busy to list everything: don't replace a good list with a short one
		info.Games = a.ps5info.Games
	}
	a.ps5info = info
	// A full scan is the truth: forget "sent" notes for images that are no
	// longer there, so a game deleted on the PS5 stops showing as on it.
	if info.Online && info.Scanned && !info.Partial {
		on := map[string]bool{}
		for _, cs := range info.Games {
			for _, c := range cs {
				on[strings.ToLower(path.Base(c.Path))] = true
			}
		}
		changed := false
		for k, si := range a.sent {
			if si.Host == host && !on[k] {
				delete(a.sent, k)
				changed = true
			}
		}
		if changed {
			saveJSON("sent.json", a.sent)
		}
	}
	// keep the save list fresh too, so the Library can show which games have saves
	if info.Online && time.Since(a.saves.Scanned) > 5*time.Minute {
		go a.refreshSaves()
	}
	var missing []string
	for id := range info.Games {
		if !info.Icons[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		go func() {
			if fetchStoreIcons(missing) {
				a.mu.Lock()
				if a.ps5info.Icons != nil {
					for _, id := range missing {
						if fileExists(ps5IconPath(id)) {
							a.ps5info.Icons[id] = true
						}
					}
				}
				a.mu.Unlock()
			}
		}()
	}
}

func (a *App) ps5Loop() {
	time.Sleep(time.Second)
	for {
		a.refreshPS5()
		time.Sleep(60 * time.Second)
	}
}

// checkRoom stops a send early when the drive is too full for the image.
func (a *App) checkRoom(f *ftpConn, host, dir, img, label string) error {
	fi, err := os.Stat(img)
	if err != nil {
		return nil
	}
	free, slack := freeOn(host, dir)
	if free < 0 {
		return nil // no shell running, so we can't tell
	}
	base := filepath.Base(img)
	var have int64
	if ents, err := f.list(dir); err == nil {
		for _, e := range ents {
			if e.Name == base && e.Size == fi.Size() {
				return nil // already there
			}
			if e.Name == base+".part" && e.Size <= fi.Size() {
				have = e.Size
			}
		}
	}
	if need := fi.Size() - have; need > free+slack {
		if label == "" {
			label, _, _ = driveLabel(rootOf(dir))
		}
		return fmt.Errorf("not enough space on the PS5's %s: %s free, this image needs %s", label, humanSize(free), humanSize(need))
	}
	return nil
}
