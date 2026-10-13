package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// What the PS5's autoloader loads after a jailbreak. An autoloader keeps a
// folder named ps5_autoloader (on a USB stick or in /data) with the payload
// files and an autoload.txt naming them in order. Loadout reads that folder
// over FTP, works out each payload's version, and can swap in a newer file.

// knownRepos maps a payload's file name to the GitHub project that publishes it.
var knownRepos = []struct {
	Match, Name, Repo string
}{
	{"ftpsrv", "ftpsrv (FTP server)", "ps5-payload-dev/ftpsrv"},
	{"shsrv", "shsrv (shell)", "ps5-payload-dev/shsrv"},
	{"websrv", "websrv (homebrew launcher)", "ps5-payload-dev/websrv"},
	{"klogsrv", "klogsrv (kernel log)", "ps5-payload-dev/klogsrv"},
	{"gdbsrv", "gdbsrv (debugger)", "ps5-payload-dev/gdbsrv"},
	{"shadowmount", "ShadowMountPlus", "drakmor/ShadowMountPlus"},
	{"kstuff", "kstuff", "EchoStretch/kstuff"},
	{"etahen", "etaHEN", "etaHEN/etaHEN"},
}

type AutoItem struct {
	File    string `json:"file"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Listed  bool   `json:"listed"`  // named in autoload.txt
	Missing bool   `json:"missing"` // named in autoload.txt but not in the folder
	Name    string `json:"name"`    // friendly name when Loadout knows the payload
	Repo    string `json:"repo"`
	Version string `json:"version"` // read out of the file, "" when it doesn't say
	Latest  string `json:"latest"`  // newest release on GitHub, "" when unknown
	Asset   string `json:"asset"`   // download link for the matching file in that release
	Same    bool   `json:"same"`    // the PS5's file is byte-for-byte the newest release
	Note    string `json:"note"`
	Desc    string `json:"desc"`
	Source  string `json:"source"` // which catalog the payload came from
	Via     string `json:"via"`    // where an update would come from: "catalog" or "github"
	sum     string // the catalog's checksum for the newest file
	newFile string // the catalog's file name for the newest version
}

type AutoInfo struct {
	Dir        string     `json:"dir"`
	Order      []string   `json:"order"` // autoload.txt, line by line
	Items      []AutoItem `json:"items"`
	Tried      []string   `json:"tried"`
	Manager    bool       `json:"manager"`    // read from PS5 Payload Manager's own folder
	ManagerURL string     `json:"managerUrl"` // its dashboard on the PS5
	Seen       []string   `json:"seen"`       // what the PS5's top folders hold, when nothing was found
}

func autoloadDirs() []string {
	dirs := []string{}
	for i := 0; i < 8; i++ {
		dirs = append(dirs, fmt.Sprintf("/mnt/usb%d/ps5_autoloader", i))
	}
	return append(dirs, "/data/ps5_autoloader", "/mnt/ext0/ps5_autoloader", "/mnt/ext1/ps5_autoloader")
}

var verToken = regexp.MustCompile(`^[vV]?\d+\.\d+(\.\d+){0,2}([-_. ]?(beta|alpha|rc|b|a)[-_. ]?\d*)?$`)
var verLoose = regexp.MustCompile(`(?i)\bv(?:ersion)?[ :=]*v?(\d+\.\d+(?:\.\d+)?(?:[-_ ]?(?:beta|alpha|rc)\d*)?)`)

// payloadVersion looks for a version written inside a payload file.
func payloadVersion(b []byte) string {
	counts := map[string]int{}
	var loose string
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		s := string(b[start:end])
		start = -1
		if len(s) < 3 || len(s) > 200 {
			return
		}
		if verToken.MatchString(s) && strings.ContainsAny(s[:1], "vV") {
			counts[s]++
		} else if loose == "" {
			low := strings.ToLower(s)
			skip := false
			for _, w := range []string{"clang", "gcc", "llvm", "http", "license", "foundation", "firmware", "kernel", "sdk", "openssl", "zlib", "sqlite", "curl"} {
				if strings.Contains(low, w) {
					skip = true // a compiler or library talking about itself, not the payload
				}
			}
			if m := verLoose.FindStringSubmatch(s); m != nil && !skip && strings.Contains(low, "version") {
				loose = "v" + m[1]
			}
		}
	}
	for i, c := range b {
		if c >= 0x20 && c < 0x7f {
			if start < 0 {
				start = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(b))
	best, n := "", 0
	for s, c := range counts {
		if c > n || (c == n && s > best) {
			best, n = s, c
		}
	}
	if best != "" {
		return best
	}
	return loose
}

type ghRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

var ghCache = map[string]*ghRelease{}
var ghCacheAt = map[string]time.Time{}

func ghBase() string {
	if b := os.Getenv("TT_GHAPI"); b != "" { // test stand-in
		return b
	}
	return "https://api.github.com"
}

func latestRelease(repo string) (*ghRelease, error) {
	recvMu.Lock()
	if r := ghCache[repo]; r != nil && time.Since(ghCacheAt[repo]) < 30*time.Minute {
		recvMu.Unlock()
		return r, nil
	}
	recvMu.Unlock()
	req, _ := http.NewRequest("GET", ghBase()+"/repos/"+repo+"/releases?per_page=5", nil)
	req.Header.Set("User-Agent", "Loadout/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := storeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("GitHub said " + resp.Status)
	}
	var rs []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rs); err != nil || len(rs) == 0 {
		return nil, errors.New("no releases listed")
	}
	recvMu.Lock()
	ghCache[repo], ghCacheAt[repo] = &rs[0], time.Now()
	recvMu.Unlock()
	return &rs[0], nil
}

func httpBytes(url string, max int64) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Loadout/"+appVersion)
	cl := &http.Client{Timeout: 120 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("download failed: " + resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

func isPayloadFile(n string) bool {
	switch strings.ToLower(path.Ext(n)) {
	case ".elf", ".bin", ".self", ".lua", ".jar":
		return true
	}
	return false
}

// scanAutoload reads the autoloader folder. With online, it also asks GitHub
// for the newest release of each payload it recognizes.
// findAutoload looks for the autoloader's folder: the usual name first, then
// any folder on the PS5's drives that is named like one or holds an
// autoload list. seen records what each place held, for troubleshooting.
func findAutoload(f *ftpConn, custom string) (dir string, ents []ftpEntry, tried []string, seen []string) {
	try := func(d string) bool {
		tried = append(tried, d)
		es, err := f.list(d)
		if err != nil {
			return false
		}
		dir, ents = d, es
		return true
	}
	if custom != "" && try(custom) {
		return
	}
	for _, d := range autoloadDirs() {
		if try(d) {
			return
		}
	}
	roots := []string{"/data", "/data/etaHEN", "/data/homebrew"}
	for i := 0; i < 8; i++ {
		roots = append(roots, fmt.Sprintf("/mnt/usb%d", i))
	}
	roots = append(roots, "/mnt/ext0", "/mnt/ext1")
	var best string
	var bestEnts []ftpEntry
	for _, r := range roots {
		es, err := f.list(r)
		if err != nil {
			continue
		}
		var names []string
		hasList := false
		for _, e := range es {
			names = append(names, e.Name)
			if !e.IsDir && strings.HasPrefix(strings.ToLower(e.Name), "autoload") {
				hasList = true
			}
		}
		if len(names) > 40 {
			names = append(names[:40], "…")
		}
		seen = append(seen, r+": "+strings.Join(names, ", "))
		if hasList && best == "" {
			best, bestEnts = r, es
		}
		for _, e := range es {
			low := strings.ToLower(e.Name)
			if !e.IsDir || !(strings.Contains(low, "autoload") || low == "payloads" || low == "payload" || low == "elfs") {
				continue
			}
			d := path.Join(r, e.Name)
			tried = append(tried, d)
			sub, err := f.list(d)
			if err != nil {
				continue
			}
			score := 0
			for _, x := range sub {
				if !x.IsDir && strings.HasPrefix(strings.ToLower(x.Name), "autoload") {
					score += 10
				}
				if !x.IsDir && isPayloadFile(x.Name) {
					score++
				}
			}
			if score >= 10 || (score > 0 && best == "") {
				best, bestEnts = d, sub
				if score >= 10 {
					return best, bestEnts, tried, seen
				}
			}
		}
	}
	return best, bestEnts, tried, seen
}

// scanAutoload reads the autoloader folder. With online, it also asks GitHub
// for the newest release of each payload it recognizes.
func scanAutoload(f *ftpConn, online bool, custom string) (*AutoInfo, error) {
	if custom == "" {
		if info := scanManager(f, online); info != nil {
			return info, nil
		}
	}
	info := &AutoInfo{Order: []string{}, Items: []AutoItem{}}
	var ents []ftpEntry
	info.Dir, ents, info.Tried, info.Seen = findAutoload(f, custom)
	if info.Dir == "" {
		return info, nil
	}
	orderFile := "autoload.txt"
	for _, e := range ents {
		if !e.IsDir && strings.HasPrefix(strings.ToLower(e.Name), "autoload") {
			orderFile = e.Name
		}
	}
	listed := map[string]bool{}
	if b, err := f.get(path.Join(info.Dir, orderFile), 64<<10); err == nil {
		for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			info.Order = append(info.Order, l)
			if !strings.HasPrefix(l, "!") && !strings.HasPrefix(l, "#") {
				listed[strings.ToLower(l)] = true
			}
		}
	}
	have := map[string]bool{}
	for _, e := range ents {
		if e.IsDir || !isPayloadFile(e.Name) {
			continue
		}
		have[strings.ToLower(e.Name)] = true
		it := AutoItem{File: e.Name, Path: path.Join(info.Dir, e.Name), Size: e.Size, Listed: listed[strings.ToLower(e.Name)]}
		low := strings.ToLower(e.Name)
		for _, k := range knownRepos {
			if strings.Contains(low, k.Match) {
				it.Name, it.Repo = k.Name, k.Repo
				break
			}
		}
		var data []byte
		if e.Size > 0 && e.Size <= 48<<20 {
			data, _ = f.get(it.Path, 48<<20)
			if int64(len(data)) == e.Size {
				it.Version = payloadVersion(data)
			} else {
				data = nil
			}
		}
		if online && it.Repo != "" {
			if rel, err := latestRelease(it.Repo); err == nil {
				it.Latest = rel.Tag
				for _, as := range rel.Assets {
					if strings.EqualFold(as.Name, e.Name) {
						it.Asset = as.URL
						if data != nil && as.Size == e.Size {
							if nb, err := httpBytes(as.URL, 64<<20); err == nil {
								it.Same = sha256.Sum256(nb) == sha256.Sum256(data)
							}
						}
					}
				}
				if it.Asset == "" {
					it.Note = "The newest release doesn't have a file with this exact name, so update it by hand."
				}
			} else {
				it.Note = "Couldn't check GitHub for the newest version (" + err.Error() + ")."
			}
		}
		info.Items = append(info.Items, it)
	}
	for n := range listed {
		if !have[n] {
			info.Items = append(info.Items, AutoItem{File: n, Listed: true, Missing: true, Note: "Named in autoload.txt but not in the folder."})
		}
	}
	// autoload order first, then the rest by name
	pos := map[string]int{}
	for i, l := range info.Order {
		pos[strings.ToLower(l)] = i + 1
	}
	sort.SliceStable(info.Items, func(i, j int) bool {
		a, b := pos[strings.ToLower(info.Items[i].File)], pos[strings.ToLower(info.Items[j].File)]
		if (a > 0) != (b > 0) {
			return a > 0
		}
		if a != b {
			return a < b
		}
		return strings.ToLower(info.Items[i].File) < strings.ToLower(info.Items[j].File)
	})
	return info, nil
}

func (a *App) autoloadRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/autoload/scan", func(r *http.Request) (any, error) {
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		a.mu.Lock()
		custom := a.cfg.AutoDir
		a.mu.Unlock()
		return scanAutoload(f, true, custom)
	})
	api("/api/autoload/setdir", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
		d := strings.TrimSpace(strings.ReplaceAll(b.Dir, "\\", "/"))
		if d != "" {
			d = path.Clean("/" + d)
			if d == "/" || strings.ContainsAny(d, "\r\n") {
				return nil, errors.New("Type the full folder path, like /data/payloads.")
			}
		}
		a.mu.Lock()
		a.cfg.AutoDir = d
		saveJSON("settings.json", a.cfg)
		a.mu.Unlock()
		return nil, nil
	})
	// replace one autoloaded payload with the newest release of the same file
	api("/api/autoload/update", func(r *http.Request) (any, error) {
		var b struct{ File, Path string }
		body(r, &b)
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		a.mu.Lock()
		custom := a.cfg.AutoDir
		a.mu.Unlock()
		info, err := scanAutoload(f, true, custom)
		if err != nil {
			return nil, err
		}
		for _, it := range info.Items {
			if it.File != b.File || it.Missing || (b.Path != "" && it.Path != b.Path) {
				continue
			}
			if info.Manager {
				return managerUpdate(f, it)
			}
			if it.Asset == "" || !strings.HasPrefix(it.Asset, "https://github.com/"+it.Repo+"/") {
				return nil, errors.New("There's no matching download for this payload, so it has to be updated by hand.")
			}
			data, err := httpBytes(it.Asset, 64<<20)
			if err != nil {
				return nil, fmt.Errorf("Couldn't download the new %s: %v", it.File, err)
			}
			if len(data) < 1024 || (strings.EqualFold(path.Ext(it.File), ".elf") && !bytes.HasPrefix(data, []byte("\x7fELF"))) {
				return nil, errors.New("The download doesn't look like a payload, so nothing was changed.")
			}
			tmp, err := os.CreateTemp("", "loadout-payload")
			if err != nil {
				return nil, err
			}
			defer os.Remove(tmp.Name())
			tmp.Write(data)
			tmp.Close()
			// keep the old file beside the new one, in case the new one misbehaves
			bak := it.Path + ".old"
			f.cmd("DELE %s", bak)
			if err := f.rename(it.Path, bak); err != nil {
				return nil, fmt.Errorf("Couldn't set the old file aside: %v", err)
			}
			if _, err := f.uploadAs(tmp.Name(), info.Dir, it.File, true, func(int64, int64) {}, func() bool { return false }); err != nil {
				f.rename(bak, it.Path) // put the old one back
				return nil, fmt.Errorf("Couldn't copy the new file to the PS5, so the old one was put back: %v", err)
			}
			sum := sha256.Sum256(data)
			return map[string]any{"file": it.File, "latest": it.Latest, "sha": hex.EncodeToString(sum[:8])}, nil
		}
		return nil, errors.New("That payload isn't in the autoloader folder any more.")
	})
}

// managerGitHub looks a payload up on its GitHub project when Payload
// Manager's catalog doesn't cover it. It reports whether a project matched.
func managerGitHub(it *AutoItem, size int64) bool {
	low := strings.ToLower(it.File)
	for _, k := range knownRepos {
		if !strings.Contains(low, k.Match) {
			continue
		}
		it.Repo = k.Repo
		if it.Name == "" {
			it.Name = k.Name
		}
		rel, err := latestRelease(k.Repo)
		if err != nil {
			it.Note = "Couldn't check GitHub for the newest version (" + err.Error() + ")."
			return true
		}
		it.Latest = rel.Tag
		for _, as := range rel.Assets {
			if strings.EqualFold(as.Name, it.File) && strings.HasPrefix(as.URL, "https://github.com/"+k.Repo+"/") {
				it.Asset, it.Via = as.URL, "github"
			}
		}
		if it.Asset == "" {
			it.Note = "The newest release on GitHub doesn't have a file with this exact name, so update it by hand."
		}
		return true
	}
	return false
}

// managerUpdate swaps a newer payload into Payload Manager's folder and
// brings its info file up to date. The old file is kept on this PC.
func managerUpdate(f *ftpConn, it AutoItem) (map[string]any, error) {
	if !dlOK(it.Asset) {
		return nil, errors.New("There's no download for this payload, so it has to be updated in Payload Manager.")
	}
	data, err := httpBytes(it.Asset, 64<<20)
	if err != nil {
		return nil, fmt.Errorf("Couldn't download the new %s: %v", it.File, err)
	}
	if len(data) < 1024 || (strings.EqualFold(path.Ext(it.File), ".elf") && !bytes.HasPrefix(data, []byte("\x7fELF"))) {
		return nil, errors.New("The download doesn't look like a payload, so nothing was changed.")
	}
	sha := sha256.Sum256(data)
	md := md5.Sum(data)
	shaHex, mdHex := hex.EncodeToString(sha[:]), hex.EncodeToString(md[:])
	if want := strings.ToLower(strings.TrimSpace(it.sum)); want != "" {
		if i := strings.IndexByte(want, ':'); i >= 0 { // "sha256:abcd..."
			want = want[i+1:]
		}
		if (len(want) == 64 && want != shaHex) || (len(want) == 32 && want != mdHex) {
			return nil, errors.New("The download doesn't match the checksum in the payload catalog, so nothing was changed.")
		}
	}
	// keep the current file on this PC first
	old, err := f.get(it.Path, 64<<20)
	if err != nil || int64(len(old)) != it.Size {
		return nil, errors.New("Couldn't copy the current file off the PS5 as a backup, so nothing was changed.")
	}
	bdir := filepath.Join(appDir(), "PayloadBackups")
	os.MkdirAll(bdir, 0755)
	bak := filepath.Join(bdir, time.Now().Format("2006-01-02 15.04.05")+" "+it.File)
	if err := os.WriteFile(bak, old, 0644); err != nil {
		return nil, fmt.Errorf("Couldn't save the backup, so nothing was changed: %v", err)
	}
	tmp, err := os.CreateTemp("", "loadout-payload")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	tmp.Write(data)
	tmp.Close()
	// the new version goes in under its own file name when the catalog gives
	// one (ftpsrv_v0.21.elf, say), and autoload.txt is pointed at it
	dir := path.Dir(it.Path)
	name := it.File
	if n := path.Base(strings.ReplaceAll(strings.TrimSpace(it.newFile), "\\", "/")); it.newFile != "" && n != "." && n != "/" && isPayloadFile(n) && !strings.ContainsAny(n, "\r\n") {
		name = n
	}
	renamed := !strings.EqualFold(name, it.File)
	newPath := path.Join(dir, name)
	if renamed {
		if _, exists := f.size(newPath); exists {
			return nil, fmt.Errorf("%s is already on the PS5, so nothing was changed.", name)
		}
	}
	if _, err := f.uploadAs(tmp.Name(), dir, name, true, func(int64, int64) {}, func() bool { return false }); err != nil {
		return nil, fmt.Errorf("Couldn't copy the new file to the PS5 (the old one is still there): %v", err)
	}
	// Payload Manager shows the version from the info file beside the payload
	if b, err := f.get(it.Path+".json", 64<<10); err == nil {
		var side map[string]any
		if json.Unmarshal(b, &side) == nil {
			if it.Latest != "" {
				side["version"] = it.Latest
			}
			if _, ok := side["url"]; ok {
				side["url"] = it.Asset
			}
			if _, ok := side["filename"]; ok {
				side["filename"] = name
			}
			if cur, ok := side["checksum"].(string); ok {
				pre := ""
				if i := strings.IndexByte(cur, ':'); i >= 0 {
					pre, cur = cur[:i+1], cur[i+1:]
				}
				switch len(cur) {
				case 64:
					side["checksum"] = pre + shaHex
				case 32:
					side["checksum"] = pre + mdHex
				}
			}
			if nb, err := json.MarshalIndent(side, "", "  "); err == nil {
				f.put(newPath+".json", nb)
			}
		}
	}
	autoloadFixed := false
	if renamed {
		// point autoload.txt at the new file name, keeping everything else as it was
		al := path.Join(pldmgrDir, "autoload.txt")
		if b, err := f.get(al, 256<<10); err == nil {
			lines := strings.Split(string(b), "\n")
			changed := false
			for i, l := range lines {
				body := strings.TrimRight(l, "\r")
				trimmed := strings.TrimSpace(body)
				ref := strings.TrimPrefix(trimmed, "!")
				if ref == "" || !strings.EqualFold(path.Base(ref), it.File) {
					continue
				}
				cut := len(body) - len(strings.TrimLeft(body, " \t"))
				lead := body[:cut]
				bang := ""
				if strings.HasPrefix(trimmed, "!") {
					bang = "!"
				}
				lines[i] = lead + bang + ref[:len(ref)-len(path.Base(ref))] + name + l[len(body):]
				changed = true
			}
			if changed {
				if err := f.put(al, []byte(strings.Join(lines, "\n"))); err != nil {
					// don't leave autoload pointing at a file that's about to go
					f.cmd("DELE %s", newPath)
					f.cmd("DELE %s", newPath+".json")
					return nil, fmt.Errorf("Couldn't update Payload Manager's autoload list, so the old payload was left in place: %v", err)
				}
				autoloadFixed = true
			}
		}
		f.cmd("DELE %s", it.Path)
		f.cmd("DELE %s", it.Path+".json")
	}
	return map[string]any{"file": name, "was": it.File, "latest": it.Latest, "backup": bak, "autoload": autoloadFixed}, nil
}

// ---- PS5 Payload Manager (itsPLK/ps5-payload-manager) ----
// It keeps everything in /data/pldmgr: payloads/<folder>/<file> with a
// <file>.json beside each one (name, version, where it came from), and
// autoload.txt naming what to start. Its catalog of available payloads is a
// JSON list on the web.

const pldmgrDir = "/data/pldmgr"
const pldmgrCatalog = "https://itsplk.github.io/ps5-payloads-mirror/payloads.json"

type pldCatalog struct {
	Name     string `json:"name"`
	Payloads []struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Version  string `json:"version"`
		Checksum string `json:"checksum"`
	} `json:"payloads"`
}

func fetchCatalog(url string) (*pldCatalog, error) {
	if b := os.Getenv("TT_PLDCATALOG"); b != "" && url == pldmgrCatalog { // test stand-in
		url = b
	}
	data, err := httpBytes(url, 4<<20)
	if err != nil {
		return nil, err
	}
	var c pldCatalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, errors.New("the payload catalog couldn't be read")
	}
	return &c, nil
}

func scanManager(f *ftpConn, online bool) *AutoInfo {
	if _, err := f.list(pldmgrDir); err != nil {
		return nil
	}
	info := &AutoInfo{Dir: pldmgrDir, Manager: true, ManagerURL: "http://" + f.host + ":8084", Order: []string{}, Items: []AutoItem{}}
	listed := map[string]int{}
	if b, err := f.get(path.Join(pldmgrDir, "autoload.txt"), 64<<10); err == nil {
		for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			info.Order = append(info.Order, l)
			if !strings.HasPrefix(l, "!") {
				listed[strings.ToLower(path.Base(l))] = len(info.Order)
			}
		}
	}
	// the catalogs it knows: the official one plus any the user added
	type latest struct{ ver, sum, url, file string }
	newest := map[string]latest{}
	var catErr string
	if online {
		urls := []string{pldmgrCatalog}
		if b, err := f.get(path.Join(pldmgrDir, "sources.json"), 256<<10); err == nil {
			for _, m := range regexp.MustCompile(`"url"\s*:\s*"([^"]+)"`).FindAllStringSubmatch(string(b), -1) {
				if u := strings.ReplaceAll(m[1], `\/`, "/"); strings.HasPrefix(u, "http") && u != pldmgrCatalog {
					urls = append(urls, u)
				}
			}
		}
		for i, u := range urls {
			c, err := fetchCatalog(u)
			if err != nil {
				if i == 0 {
					catErr = err.Error()
				}
				continue
			}
			for _, p := range c.Payloads {
				k := strings.ToLower(strings.TrimSpace(p.Name))
				if _, dup := newest[k]; !dup && k != "" {
					newest[k] = latest{p.Version, strings.ToLower(p.Checksum), p.URL, p.Filename}
				}
			}
		}
	}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := f.list(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			p := path.Join(dir, e.Name)
			if e.IsDir {
				if depth < 3 {
					walk(p, depth+1)
				}
				continue
			}
			if !isPayloadFile(e.Name) {
				continue
			}
			it := AutoItem{File: e.Name, Path: p, Size: e.Size}
			_, it.Listed = listed[strings.ToLower(e.Name)]
			var side struct {
				Name, Description, Version, Checksum string
				SourceName                           string `json:"source_name"`
			}
			if b, err := f.get(p+".json", 32<<10); err == nil && json.Unmarshal(b, &side) == nil {
				it.Name, it.Version, it.Desc, it.Source = side.Name, side.Version, side.Description, side.SourceName
			}
			if it.Version == "" && e.Size > 0 && e.Size <= 48<<20 {
				if data, err := f.get(p, 48<<20); err == nil && int64(len(data)) == e.Size {
					it.Version = payloadVersion(data)
				}
			}
			if n, ok := newest[strings.ToLower(strings.TrimSpace(it.Name))]; ok && it.Name != "" {
				it.Latest = n.ver
				it.Same = (n.sum != "" && n.sum == strings.ToLower(side.Checksum)) || (n.ver != "" && strings.EqualFold(n.ver, it.Version))
				if dlOK(n.url) {
					it.Asset, it.Via, it.sum, it.newFile = n.url, "catalog", n.sum, n.file
				}
			} else if online && managerGitHub(&it, e.Size) {
				// not in the catalog, but it's a payload Loadout knows the GitHub project of
			} else if online && catErr != "" {
				it.Note = "Couldn't reach the payload catalog to check for a newer version (" + catErr + ")."
			} else if online && it.Name == "" {
				it.Note = "Added by hand, so there's no catalog entry to compare with."
			}
			info.Items = append(info.Items, it)
		}
	}
	walk(pldmgrDir, 0)
	have := map[string]bool{}
	for _, it := range info.Items {
		have[strings.ToLower(it.File)] = true
	}
	for n := range listed {
		if !have[n] {
			info.Items = append(info.Items, AutoItem{File: n, Listed: true, Missing: true, Note: "Set to autoload, but the file isn't on the PS5."})
		}
	}
	sort.SliceStable(info.Items, func(i, j int) bool {
		a, b := listed[strings.ToLower(info.Items[i].File)], listed[strings.ToLower(info.Items[j].File)]
		if (a > 0) != (b > 0) {
			return a > 0
		}
		if a != b {
			return a < b
		}
		return strings.ToLower(info.Items[i].File) < strings.ToLower(info.Items[j].File)
	})
	return info
}

// dlOK: downloads must come over https (plain http only for the test stand-in).
func dlOK(u string) bool {
	return strings.HasPrefix(u, "https://") || (os.Getenv("TT_PLDCATALOG") != "" && strings.HasPrefix(u, "http://127.0.0.1"))
}
