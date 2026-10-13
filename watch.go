package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The downloads folder: a catch-all folder (games, movies, anything) that
// Loadout checks every few minutes for PS5 games (and Switch games, which go
// to the ROM library) packed in archives, the way
// releases come (split RAR sets: .rar + .r00..., or .part01.rar...; also ZIP
// and 7z). A set is extracted once its download is finished: every part an
// .sfv lists is there and nothing in its folder has changed for a while.
// Only archives with a PS5 game in them (sce_sys/param.json) are touched,
// and the extracting is done by 7-Zip, which handles all of those formats.
// Each game lands in the games folder under its release name; it's
// extracted under a hidden name first, so a scan never sees half a game.

const (
	watchEvery  = 5 * time.Minute
	watchSettle = 3 * time.Minute // a download is done once its files are this old
)

type WatchInfo struct {
	On      bool     `json:"on"`
	Folder  string   `json:"folder"`
	Busy    bool     `json:"busy"`    // extracting right now
	Current string   `json:"current"` // what
	Pct     int      `json:"pct"`     // 0..1000
	Checked string   `json:"checked"` // when the folder was last looked at
	Waiting []string `json:"waiting"` // games found that are still downloading
	Done    []string `json:"done"`    // extracted while Loadout has been running
	Err     string   `json:"err"`
	NoTool  bool     `json:"noTool"` // 7-Zip isn't installed
}

// watchRecord is what was done with each archive set, so it's done once.
type watchRecord struct {
	Size   int64     // the set's size when it was handled
	When   time.Time `json:",omitempty"`
	Result string    // "extracted", "not a PS5 game", or why it failed
}

var (
	watchMu      sync.Mutex // one check at a time
	firstPartRe  = regexp.MustCompile(`(?i)\.part0*1\.rar$`)
	laterPartRe  = regexp.MustCompile(`(?i)\.part0*([2-9]|\d{2,})\.rar$`)
	splitNumRe   = regexp.MustCompile(`(?i)\.(zip|7z)\.0*1$`)
	pctRe        = regexp.MustCompile(`(\d{1,3})%`)
	sevenTotalRe = regexp.MustCompile(`^\S+\s+\S+\s+(\d+)\s+\d*\s*\d+ files?`)
	inProgressRe = regexp.MustCompile(`(?i)\.(part|crdownload|!qb|!ut|tmp|aria2|download|partial)$`)
	ps5NameRe    = regexp.MustCompile(`(?i)(^|[._ -])PS5([._ -]|$)`)
)

// sevenZip finds 7-Zip's command-line program.
func sevenZip() string {
	for _, p := range []string{os.Getenv("ProgramFiles") + `\7-Zip\7z.exe`, os.Getenv("ProgramFiles(x86)") + `\7-Zip\7z.exe`, `C:\Program Files\7-Zip\7z.exe`} {
		if fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("7z"); err == nil {
		return p
	}
	return ""
}

func loadWatchRecords() map[string]watchRecord {
	recs := map[string]watchRecord{}
	if b, err := os.ReadFile(filepath.Join(appDir(), "extracted.json")); err == nil {
		json.Unmarshal(b, &recs)
	}
	return recs
}

// archiveSet is one archive (all its parts) found in the downloads folder.
type archiveSet struct {
	first  string   // the part 7-Zip opens
	parts  []string // every part, for size and settling
	name   string   // the release name the game is filed under
	size   int64
	newest time.Time
}

// findArchiveSets lists the archive sets in dir and one level of folders under it.
func findArchiveSets(root string) []archiveSet {
	var sets []archiveSet
	var look func(dir string, depth int)
	look = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var files []os.DirEntry
		for _, e := range ents {
			if e.IsDir() {
				if depth < 1 && !strings.HasPrefix(e.Name(), ".") {
					look(filepath.Join(dir, e.Name()), depth+1)
				}
				continue
			}
			files = append(files, e)
		}
		for _, e := range files {
			n := e.Name()
			low := strings.ToLower(n)
			first := false
			switch {
			case firstPartRe.MatchString(n), splitNumRe.MatchString(n):
				first = true
			case strings.HasSuffix(low, ".rar") && !laterPartRe.MatchString(n):
				first = true // an old-style set (.rar, .r00, .r01...) or a single .rar
			case strings.HasSuffix(low, ".zip") || strings.HasSuffix(low, ".7z"):
				first = true
			}
			if !first {
				continue
			}
			stem := setStem(n)
			s := archiveSet{first: filepath.Join(dir, n)}
			for _, f := range files {
				if strings.EqualFold(setStem(f.Name()), stem) || (strings.HasPrefix(strings.ToLower(f.Name()), strings.ToLower(stem)+".") && isVolumeExt(f.Name())) {
					s.parts = append(s.parts, filepath.Join(dir, f.Name()))
					if fi, err := f.Info(); err == nil {
						s.size += fi.Size()
						if fi.ModTime().After(s.newest) {
							s.newest = fi.ModTime()
						}
					}
				}
			}
			// filed under the folder it came in, or the archive's own name when loose
			s.name = filepath.Base(dir)
			if dir == root {
				s.name = stem
			}
			sets = append(sets, s)
		}
	}
	look(root, 0)
	return sets
}

// setStem is an archive part's name without its part number and extension.
func setStem(n string) string {
	low := strings.ToLower(n)
	switch {
	case firstPartRe.MatchString(n) || laterPartRe.MatchString(n):
		return n[:strings.LastIndex(low, ".part")]
	case splitNumRe.MatchString(n) || regexp.MustCompile(`(?i)\.(zip|7z)\.\d+$`).MatchString(n):
		i := strings.LastIndex(n, ".")
		return n[:strings.LastIndex(n[:i], ".")]
	}
	if i := strings.LastIndex(n, "."); i > 0 {
		return n[:i]
	}
	return n
}

// isVolumeExt: .rar .r00 .s00 .t00 ... (old-style RAR volumes) and .001 ...
func isVolumeExt(n string) bool {
	ext := strings.ToLower(filepath.Ext(n))
	if ext == ".rar" || ext == ".zip" || ext == ".7z" {
		return true
	}
	if len(ext) == 4 && ext[1] >= 'r' && ext[1] <= 'z' && ext[2] >= '0' && ext[2] <= '9' && ext[3] >= '0' && ext[3] <= '9' {
		return true
	}
	_, err := strconv.Atoi(strings.TrimPrefix(ext, "."))
	return err == nil && len(ext) == 4
}

// downloadDone: no file in the set's folder is still being written, every
// part an .sfv there lists is present, and nothing has changed for a while.
func downloadDone(s archiveSet) (bool, string) {
	dir := filepath.Dir(s.first)
	ents, _ := os.ReadDir(dir)
	have := map[string]bool{}
	for _, e := range ents {
		have[strings.ToLower(e.Name())] = true
		if inProgressRe.MatchString(e.Name()) {
			return false, "still downloading"
		}
	}
	for _, e := range ents {
		if !strings.EqualFold(filepath.Ext(e.Name()), ".sfv") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, ";") {
				continue
			}
			if i := strings.LastIndex(line, " "); i > 0 {
				if f := strings.TrimSpace(line[:i]); !have[strings.ToLower(f)] {
					return false, "waiting for " + f
				}
			}
		}
	}
	if time.Since(s.newest) < watchSettle {
		return false, "still downloading"
	}
	return true, ""
}

// archiveLook is what 7-Zip's listing of an archive says.
type archiveLook struct {
	ps5        bool   // holds a PS5 game (sce_sys/param.json)
	id         string // its title ID
	size       int64  // unpacked
	switchGame bool   // holds a Switch game (.nsp, .xci, .nsz, .xcz)
}

// archiveInfo lists an archive with 7-Zip to see what's inside.
func archiveInfo(tool, first string) (archiveLook, error) {
	var l archiveLook
	// a big set (hundreds of parts) can take 7-Zip many minutes to list
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, tool, "l", first)
	hideChild(c)
	out, err := c.Output()
	if ctx.Err() != nil {
		return l, errors.New("7-Zip took too long to list it")
	}
	if err != nil {
		return l, fmt.Errorf("7-Zip couldn't read it: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		low := strings.ToLower(strings.ReplaceAll(t, "/", `\`))
		if strings.HasSuffix(low, `sce_sys\param.json`) {
			l.ps5 = true
			if l.id == "" {
				l.id = findID(t)
			}
		}
		if switchExtRe.MatchString(t) {
			l.switchGame = true
		}
		if m := sevenTotalRe.FindStringSubmatch(t); m != nil {
			l.size, _ = strconv.ParseInt(m[1], 10, 64)
		}
	}
	return l, nil
}

// inLibraryLocked says whether a game is in the library already (a folder
// that's here or an image of it), so its download isn't extracted again.
func (a *App) inLibraryLocked(id string) bool {
	if id == "" {
		return false
	}
	for _, e := range a.lib.Games {
		if strings.EqualFold(e.TitleID, id) && (e.Present || (e.Image != "" && fileExists(e.Image))) {
			return true
		}
	}
	_, ok := a.imageIndex()["id:"+strings.ToUpper(id)]
	return ok
}

// watchLoop checks the downloads folder now and then.
func (a *App) watchLoop() {
	time.Sleep(20 * time.Second)
	for {
		a.checkDownloads()
		time.Sleep(watchEvery)
	}
}

// checkDownloads extracts the PS5 games that finished downloading into the
// games folder, and files Switch games in the ROM library.
func (a *App) checkDownloads() {
	if !watchMu.TryLock() {
		return // a check (maybe a long extraction) is already running
	}
	defer watchMu.Unlock()
	a.mu.Lock()
	folder, games, roms := strings.TrimSpace(a.cfg.WatchFolder), a.watchDestLocked(), strings.TrimSpace(a.cfg.RomFolder)
	on := folder != "" && boolOr(a.cfg.WatchOn, true)
	a.watch.On, a.watch.Folder = on, folder
	a.mu.Unlock()
	if !on {
		return
	}
	tool := sevenZip()
	a.mu.Lock()
	a.watch.NoTool = tool == ""
	a.watch.Checked = time.Now().Format("3:04 PM")
	a.mu.Unlock()
	if tool == "" {
		return
	}
	recs := loadWatchRecords()
	var waiting []string
	noRoms := func(name string) {
		waiting = append(waiting, name+": a Switch game. Pick your ROM library (Emulators › ROM library) and it goes there")
	}

	// Switch games that are already files (downloaded as is, or extracted by hand)
	for _, f := range switchFiles(folder) {
		key := strings.ToLower(f.path)
		if r, ok := recs[key]; ok && r.Size == f.size {
			continue
		}
		if !f.settled {
			waiting = append(waiting, filepath.Base(f.path)+": still downloading")
			continue
		}
		if roms == "" {
			noRoms(filepath.Base(f.path))
			continue
		}
		recs[key] = watchRecord{Size: f.size, When: time.Now(), Result: a.fileSwitchGame(f.path, roms)}
		saveRecords(recs)
	}

	for _, s := range findArchiveSets(folder) {
		key := strings.ToLower(s.first)
		if r, ok := recs[key]; ok && r.Size == s.size {
			continue // handled already, and it hasn't changed since
		}
		done, why := downloadDone(s)
		named := ps5NameRe.MatchString(s.name) || ps5NameRe.MatchString(filepath.Base(s.first))
		nsw := nswNameRe.MatchString(s.name) || nswNameRe.MatchString(filepath.Base(s.first))
		if !done {
			if named || nsw {
				waiting = append(waiting, s.name+": "+why)
			}
			continue
		}
		// a release named as a PS5 one is taken at its word, with the title ID
		// from its .nfo; anything else is listed to see what's inside
		var look archiveLook
		if named {
			look = archiveLook{ps5: true, size: s.size + s.size/20}
			look.id, _ = readNFO(filepath.Dir(s.first))
		} else {
			var err error
			if look, err = archiveInfo(tool, s.first); err != nil {
				if strings.Contains(err.Error(), "too long") {
					recs[key] = watchRecord{Size: s.size, Result: "not checked: " + err.Error()}
					saveRecords(recs)
				}
				continue // a part is missing or it's damaged; try again next time
			}
		}
		switch {
		case look.ps5:
			if games == "" {
				continue
			}
			a.mu.Lock()
			have := a.inLibraryLocked(look.id)
			a.mu.Unlock()
			res := "already in the library (" + look.id + ")"
			if !have {
				res = a.extractGame(tool, s, look.size, games)
			}
			recs[key] = watchRecord{Size: s.size, When: time.Now(), Result: res}
		case look.switchGame || nsw:
			if len(switchFilesIn(filepath.Dir(s.first))) > 0 {
				// extracted already, next to its parts: that file is filed (above)
				recs[key] = watchRecord{Size: s.size, When: time.Now(), Result: "extracted already"}
				break
			}
			if roms == "" {
				noRoms(s.name)
				continue
			}
			recs[key] = watchRecord{Size: s.size, When: time.Now(), Result: a.extractSwitch(tool, s, look.size, roms)}
		default:
			recs[key] = watchRecord{Size: s.size, Result: "not a PS5 or Switch game"}
		}
		saveRecords(recs)
	}
	sort.Strings(waiting)
	a.mu.Lock()
	a.watch.Waiting = waiting
	a.mu.Unlock()
}

func saveRecords(recs map[string]watchRecord) {
	b, _ := json.MarshalIndent(recs, "", "  ")
	os.WriteFile(filepath.Join(appDir(), "extracted.json"), b, 0644)
}

// run7z extracts an archive set into dir, showing its progress as name.
func (a *App) run7z(tool, first, dir, name string) error {
	a.mu.Lock()
	a.watch.Busy, a.watch.Current, a.watch.Pct, a.watch.Err = true, name, 0, ""
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.watch.Busy, a.watch.Current, a.watch.Pct = false, "", 0
		a.mu.Unlock()
	}()
	c := exec.Command(tool, "x", first, "-o"+dir, "-y", "-bsp1", "-bso0", "-bse1")
	hideChild(c)
	out, _ := c.StdoutPipe()
	var errText strings.Builder
	c.Stderr = &errText
	if err := c.Start(); err != nil {
		return err
	}
	// 7-Zip redraws its progress with backspaces: read in chunks, keep the last percent
	buf := make([]byte, 4096)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			if m := pctRe.FindAllStringSubmatch(string(buf[:n]), -1); m != nil {
				if p, _ := strconv.Atoi(m[len(m)-1][1]); p >= 0 && p <= 100 {
					a.mu.Lock()
					a.watch.Pct = p * 10
					a.mu.Unlock()
				}
			}
		}
		if err != nil {
			break
		}
	}
	if err := c.Wait(); err != nil {
		if msg := strings.TrimSpace(errText.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// roomFor reports (and records) a lack of space for size bytes in dir.
func (a *App) roomFor(dir, name string, size int64) string {
	if free := diskFree(dir); free >= 0 && size > 0 && free < size+(1<<30) {
		msg := fmt.Sprintf("Not enough space in %s for %s: needs %s, %s free.", dir, name, humanSize(size), humanSize(free))
		a.mu.Lock()
		a.watch.Err = msg
		a.notify("%s", msg)
		a.mu.Unlock()
		return msg
	}
	return ""
}

// extractGame unpacks one set into the games folder and rescans. It answers
// what happened, for the record.
func (a *App) extractGame(tool string, s archiveSet, size int64, games string) string {
	name := strings.TrimSpace(s.name)
	final := filepath.Join(games, name)
	if _, err := os.Stat(final); err == nil {
		return "extracted" // already in the games folder (extracted by hand, say)
	}
	if msg := a.roomFor(games, name, size); msg != "" {
		return msg
	}
	tmp := filepath.Join(games, ".loadout-extracting-"+name)
	os.RemoveAll(tmp)
	if err := a.run7z(tool, s.first, tmp, name); err != nil {
		return a.extractFailed(name, tmp, err.Error())
	}
	if err := os.Rename(tmp, final); err != nil {
		return a.extractFailed(name, tmp, "couldn't move it into place: "+err.Error())
	}
	a.extracted(name, "your games folder", s.parts)
	go a.startScan("")
	return "extracted"
}

// extracted notes a finished download and recycles its archives if asked.
func (a *App) extracted(name, where string, parts []string) {
	a.mu.Lock()
	a.watch.Done = append(a.watch.Done, name)
	a.notify("Extracted %s from your downloads folder into %s.", name, where)
	recycleAfter := a.cfg.WatchRecycle
	a.mu.Unlock()
	if recycleAfter {
		for _, p := range parts {
			recycle(p, true)
		}
	}
}

// ---------- Switch games ----------

var (
	switchExtRe = regexp.MustCompile(`(?i)\.(nsp|xci|nsz|xcz)$`)
	nswNameRe   = regexp.MustCompile(`(?i)(^|[._ -])(NSW|Switch)([._ -]|$)`)
)

// switchDir is the ROM library's folder for Switch games: the one it already
// has (named Switch, NSW, ProsperoEden...), or a new "Switch".
func switchDir(roms string) string {
	ents, _ := os.ReadDir(roms)
	for _, e := range ents {
		if e.IsDir() {
			if id, _ := romEmu(e.Name()); id == edenID {
				return filepath.Join(roms, e.Name())
			}
		}
	}
	return filepath.Join(roms, "Switch")
}

type switchFile struct {
	path    string
	size    int64
	settled bool // done downloading
}

// switchFiles finds Switch game files in the downloads folder, its folders
// and theirs (a release folder with the game extracted into a folder in it).
func switchFiles(root string) []switchFile {
	var out []switchFile
	var look func(dir string, depth int)
	look = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		busy := false
		for _, e := range ents {
			if inProgressRe.MatchString(e.Name()) {
				busy = true
			}
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if depth < 2 {
					look(p, depth+1)
				}
				continue
			}
			if !switchExtRe.MatchString(e.Name()) {
				continue
			}
			if fi, err := e.Info(); err == nil {
				out = append(out, switchFile{p, fi.Size(), !busy && time.Since(fi.ModTime()) > watchSettle})
			}
		}
	}
	look(root, 0)
	return out
}

// switchFilesIn lists the Switch game files in a release folder and its folders.
func switchFilesIn(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && switchExtRe.MatchString(e.Name()) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// fileSwitchGame copies a Switch game file into the ROM library.
func (a *App) fileSwitchGame(src, roms string) string {
	dir := switchDir(roms)
	dst := filepath.Join(dir, filepath.Base(src))
	fi, err := os.Stat(src)
	if err != nil {
		return "gone"
	}
	if d, err := os.Stat(dst); err == nil && d.Size() == fi.Size() {
		return "in the ROM library already"
	}
	if msg := a.roomFor(roms, filepath.Base(src), fi.Size()); msg != "" {
		return msg
	}
	os.MkdirAll(dir, 0755)
	a.mu.Lock()
	a.watch.Busy, a.watch.Current, a.watch.Pct = true, filepath.Base(src), 0
	a.mu.Unlock()
	err = copyFile(src, dst+".part")
	if err == nil {
		err = os.Rename(dst+".part", dst)
	}
	a.mu.Lock()
	a.watch.Busy, a.watch.Current = false, ""
	a.mu.Unlock()
	if err != nil {
		os.Remove(dst + ".part")
		return a.extractFailed(filepath.Base(src), "", "couldn't copy it into the ROM library: "+err.Error())
	}
	romMu.Lock()
	romAt = time.Time{} // the PS5 app sees it on its next look
	romMu.Unlock()
	a.mu.Lock()
	a.watch.Done = append(a.watch.Done, filepath.Base(src))
	a.notify("Added %s from your downloads folder to the ROM library.", filepath.Base(src))
	a.mu.Unlock()
	a.autoSendRom(dir, filepath.Base(src))
	return "added to the ROM library"
}

// extractSwitch unpacks a Switch release and files its game into the ROM library.
func (a *App) extractSwitch(tool string, s archiveSet, size int64, roms string) string {
	name := strings.TrimSpace(s.name)
	dir := switchDir(roms)
	if size <= 0 {
		size = s.size
	}
	if msg := a.roomFor(roms, name, size); msg != "" {
		return msg
	}
	os.MkdirAll(dir, 0755)
	tmp := filepath.Join(dir, ".loadout-extracting-"+name)
	os.RemoveAll(tmp)
	if err := a.run7z(tool, s.first, tmp, name); err != nil {
		return a.extractFailed(name, tmp, err.Error())
	}
	found := switchFilesIn(tmp)
	if len(found) == 0 {
		return a.extractFailed(name, tmp, "there's no Switch game file (.nsp, .xci) in it")
	}
	var moved []string
	for _, f := range found {
		if err := os.Rename(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			return a.extractFailed(name, tmp, "couldn't move it into the ROM library: "+err.Error())
		}
		moved = append(moved, filepath.Base(f))
	}
	os.RemoveAll(tmp)
	romMu.Lock()
	romAt = time.Time{}
	romMu.Unlock()
	a.extracted(name, "the ROM library", s.parts)
	for _, f := range moved {
		a.autoSendRom(dir, f)
	}
	return "extracted to the ROM library"
}

// autoSendRom sends a Switch game just added to the ROM library on to
// ProsperoEden on the PS5, when that's switched on. The send is queued like
// one started from the PS5 app; if the PS5 is off it fails and says so, and
// the game can be sent from the PS5 app later.
func (a *App) autoSendRom(dir, file string) {
	a.mu.Lock()
	on := a.cfg.AutoSendRoms && strings.TrimSpace(a.cfg.PS5IP) != ""
	a.mu.Unlock()
	if !on {
		return
	}
	if _, err := a.startRomGet(filepath.Base(dir) + "/" + file); err != nil {
		a.mu.Lock()
		a.notify("%s is in the ROM library but couldn't be sent to the PS5: %v", file, err)
		a.mu.Unlock()
	}
}

func (a *App) extractFailed(name, tmp, why string) string {
	if tmp != "" {
		os.RemoveAll(tmp)
	}
	if len(why) > 300 {
		why = why[:300]
	}
	msg := fmt.Sprintf("Couldn't extract %s: %s", name, why)
	a.mu.Lock()
	a.watch.Err = msg
	a.notify("%s", msg)
	a.mu.Unlock()
	return msg
}

// watchDestLocked is the games folder extracted PS5 games go to: the one
// picked, while it's still one of the library's folders, else the main one.
func (a *App) watchDestLocked() string {
	if d := strings.TrimSpace(a.cfg.WatchDest); d != "" {
		for _, f := range a.cfg.MoreFolders {
			if strings.EqualFold(filepath.Clean(f), filepath.Clean(d)) {
				if st, err := os.Stat(f); err == nil && st.IsDir() {
					return f
				}
			}
		}
	}
	return strings.TrimSpace(a.cfg.Folder)
}

// setWatchDest picks where extracted PS5 games go: "" or the main games
// folder, one of the other games folders, or a new folder, which becomes one
// of the library's games folders so the games show up.
func (a *App) setWatchDest(p string) error {
	p = strings.TrimSpace(p)
	a.mu.Lock()
	main, watch := a.cfg.Folder, a.cfg.WatchFolder
	known := p == "" || strings.EqualFold(filepath.Clean(p), filepath.Clean(main))
	for _, f := range a.cfg.MoreFolders {
		if p != "" && strings.EqualFold(filepath.Clean(f), filepath.Clean(p)) {
			known = true
		}
	}
	a.mu.Unlock()
	if p != "" && watch != "" {
		if rel, err := filepath.Rel(watch, p); err == nil && !strings.HasPrefix(rel, "..") {
			return errors.New("That's inside your downloads folder. Pick a folder outside it, so the games don't land where they came from.")
		}
	}
	if !known {
		if err := a.addGameFolder(p); err != nil {
			return err
		}
	}
	a.mu.Lock()
	if strings.EqualFold(filepath.Clean(p), filepath.Clean(main)) {
		p = ""
	}
	a.cfg.WatchDest = p
	saveJSON("settings.json", a.cfg)
	a.mu.Unlock()
	return nil
}

// setWatchFolder sets (or clears) the downloads folder and checks it now.
func (a *App) setWatchFolder(p string) error {
	p = strings.TrimSpace(p)
	if p != "" {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return fmt.Errorf("That folder doesn't exist: %s", p)
		}
	}
	a.mu.Lock()
	for _, f := range append([]string{a.cfg.Folder}, a.cfg.MoreFolders...) {
		if p != "" && f != "" && strings.EqualFold(filepath.Clean(f), filepath.Clean(p)) {
			a.mu.Unlock()
			return errors.New("That's one of your games folders. Pick the folder your downloads go to.")
		}
	}
	a.cfg.WatchFolder = p
	saveJSON("settings.json", a.cfg)
	a.mu.Unlock()
	go a.checkDownloads()
	return nil
}
