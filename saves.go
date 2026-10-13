package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Game saves. Loadout copies each game's save folder from the PS5 as it is
// (the PS5 keeps saves encrypted and tied to the console and the user) and
// can put a copy back in the same place later.

type PS5Save struct {
	User     string `json:"user"` // the PS5's folder name for the user
	UserName string `json:"userName"`
	TitleID  string `json:"titleId"`
	Kind     string `json:"kind"` // "ps5" or "ps4"
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Files    int    `json:"files"`
}

type SaveBackup struct {
	ID       string    `json:"id"` // folder under the saves folder: TITLEID/stamp_user
	TitleID  string    `json:"titleId"`
	Name     string    `json:"name"`
	User     string    `json:"user"`
	UserName string    `json:"userName"`
	Kind     string    `json:"kind"`
	Path     string    `json:"path"` // where it lives on the PS5
	Host     string    `json:"host"`
	Size     int64     `json:"size"`
	Files    int       `json:"files"`
	When     time.Time `json:"when"`
	Auto     bool      `json:"auto"` // safety copy made just before a restore or delete
	Why      string    `json:"why"`  // "restore" or "delete" for safety copies
}

type SavesInfo struct {
	Saves   []PS5Save    `json:"saves"`
	Backups []SaveBackup `json:"backups"`
	Scanned time.Time    `json:"scanned"`
	Busy    bool         `json:"busy"`
	Err     string       `json:"err"`
	Dir     string       `json:"dir"`
}

var saveRoots = []struct{ dir, kind string }{{"savedata_prospero", "ps5"}, {"savedata", "ps4"}}
var titleIDRe = regexp.MustCompile(`^[A-Z]{4}\d{5}$`)
var userDirRe = regexp.MustCompile(`^[0-9a-fA-F]{6,16}$`)

func savesDir() string {
	d := filepath.Join(appDir(), "Saves")
	os.MkdirAll(d, 0755)
	return d
}

type remoteFile struct {
	rel  string
	size int64
}

// walkRemote lists every file under dir on the PS5.
func walkRemote(f *ftpConn, dir, rel string, depth int, out *[]remoteFile) error {
	if depth > 6 {
		return nil
	}
	ents, err := f.list(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		if e.IsDir {
			if err := walkRemote(f, path.Join(dir, e.Name), path.Join(rel, e.Name), depth+1, out); err != nil {
				return err
			}
			continue
		}
		*out = append(*out, remoteFile{path.Join(rel, e.Name), e.Size})
	}
	return nil
}

func scanSaves(f *ftpConn) ([]PS5Save, error) {
	users, err := f.list("/user/home")
	if err != nil {
		return nil, errors.New("couldn't read the users on the PS5. Is its FTP server running?")
	}
	var out []PS5Save
	for _, u := range users {
		if !u.IsDir || !userDirRe.MatchString(u.Name) {
			continue
		}
		home := path.Join("/user/home", u.Name)
		uname := ""
		if b, err := f.get(path.Join(home, "username"), 256); err == nil {
			uname = strings.TrimSpace(strings.Trim(string(b), "\x00"))
		}
		for _, r := range saveRoots {
			titles, err := f.list(path.Join(home, r.dir))
			if err != nil {
				continue
			}
			for _, t := range titles {
				if !t.IsDir || !titleIDRe.MatchString(t.Name) {
					continue
				}
				p := path.Join(home, r.dir, t.Name)
				var files []remoteFile
				if walkRemote(f, p, "", 0, &files) != nil || len(files) == 0 {
					continue
				}
				s := PS5Save{User: u.Name, UserName: uname, TitleID: t.Name, Kind: r.kind, Path: p, Files: len(files)}
				for _, x := range files {
					s.Size += x.size
				}
				out = append(out, s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TitleID != out[j].TitleID {
			return out[i].TitleID < out[j].TitleID
		}
		return out[i].User < out[j].User
	})
	return out, nil
}

func loadBackups() []SaveBackup {
	out := []SaveBackup{}
	root := savesDir()
	titles, _ := os.ReadDir(root)
	for _, t := range titles {
		if !t.IsDir() {
			continue
		}
		subs, _ := os.ReadDir(filepath.Join(root, t.Name()))
		for _, s := range subs {
			if !s.IsDir() || strings.HasSuffix(s.Name(), ".partial") {
				continue
			}
			var b SaveBackup
			raw, err := os.ReadFile(filepath.Join(root, t.Name(), s.Name(), "loadout-save.json"))
			if err != nil || json.Unmarshal(raw, &b) != nil {
				continue
			}
			b.ID = t.Name() + "/" + s.Name()
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	return out
}

// backupDir turns a backup ID into its folder, refusing anything outside the saves folder.
func backupDir(id string) (string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 || !titleIDRe.MatchString(parts[0]) || strings.ContainsAny(parts[1], `\/:`) || strings.HasPrefix(parts[1], ".") {
		return "", errors.New("That backup wasn't found.")
	}
	d := filepath.Join(savesDir(), parts[0], parts[1])
	if !dirExists(d) {
		return "", errors.New("That backup isn't on this PC any more.")
	}
	return d, nil
}

// refreshSaves rescans the PS5's saves and the backups on this PC.
func (a *App) refreshSaves() {
	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	if a.saves.Busy {
		a.mu.Unlock()
		return
	}
	a.saves.Busy = true
	a.saves.Dir = savesDir()
	if a.saves.Backups == nil {
		a.saves.Backups = loadBackups() // backups show even while the PS5 is off
	}
	a.mu.Unlock()
	var saves []PS5Save
	var err error
	if host == "" {
		err = errors.New("Set your PS5's IP address in Settings first.")
	} else {
		var f *ftpConn
		if f, err = ftpDial(host, port); err == nil {
			saves, err = scanSaves(f)
			f.Close()
		}
	}
	backups := loadBackups()
	a.mu.Lock()
	a.saves.Busy, a.saves.Backups, a.saves.Scanned = false, backups, time.Now()
	if err != nil {
		a.saves.Err = err.Error()
		a.saves.Saves = nil
	} else {
		a.saves.Err = ""
		a.saves.Saves = saves
	}
	a.mu.Unlock()
}

// download copies one PS5 file to this PC.
func (f *ftpConn) download(remote, local string, onBytes func(int64), cancelled func() bool) error {
	f.cmd("TYPE I")
	d, err := f.data()
	if err != nil {
		return err
	}
	defer d.Close()
	code, msg, err := f.cmd("RETR %s", remote)
	if err != nil || (code != 150 && code != 125) {
		if err == nil {
			err = errors.New(msg)
		}
		return fmt.Errorf("the PS5 won't hand over %s: %v", path.Base(remote), err)
	}
	os.MkdirAll(filepath.Dir(local), 0755)
	out, err := os.Create(local)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	var rerr error
	for {
		if cancelled != nil && cancelled() {
			rerr = errFTPCancelled
			break
		}
		d.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := d.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				rerr = werr
				break
			}
			onBytes(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			rerr = fmt.Errorf("the connection to the PS5 dropped (%v)", shortNetErr(err))
			break
		}
	}
	if cerr := out.Close(); rerr == nil {
		rerr = cerr
	}
	d.Close()
	if rerr != nil {
		f.cmd("ABOR")
		f.read()
		return rerr
	}
	if code, msg, err := f.read(); err != nil || !ok2xx(code) {
		if err == nil {
			err = errors.New(msg)
		}
		return fmt.Errorf("the PS5 didn't finish sending %s: %v", path.Base(remote), err)
	}
	return nil
}

// pullSave copies a save folder from the PS5 into a new backup folder.
func pullSave(f *ftpConn, host string, s PS5Save, name string, why string, progress func(done, total int64), cancelled func() bool) (*SaveBackup, error) {
	var files []remoteFile
	if err := walkRemote(f, s.Path, "", 0, &files); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("there's no save for this game on the PS5")
	}
	var total, done int64
	for _, x := range files {
		total += x.size
	}
	stamp := time.Now().Format("2006-01-02_150405")
	dirName := stamp + "_" + s.User
	auto := why != ""
	if auto {
		dirName += "_before-" + why
	}
	final := filepath.Join(savesDir(), s.TitleID, dirName)
	tmp := final + ".partial"
	os.RemoveAll(tmp)
	for _, x := range files {
		err := f.download(path.Join(s.Path, x.rel), filepath.Join(tmp, "files", filepath.FromSlash(x.rel)), func(n int64) {
			done += n
			progress(done, total)
		}, cancelled)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		if fi, err := os.Stat(filepath.Join(tmp, "files", filepath.FromSlash(x.rel))); err != nil || fi.Size() != x.size {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s came through incomplete. Close the game on the PS5 and try again", x.rel)
		}
	}
	b := &SaveBackup{TitleID: s.TitleID, Name: name, User: s.User, UserName: s.UserName, Kind: s.Kind, Path: s.Path, Host: host, Size: total, Files: len(files), When: time.Now(), Auto: auto, Why: why}
	raw, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "loadout-save.json"), raw, 0644); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	b.ID = s.TitleID + "/" + dirName
	return b, nil
}

type saveReq struct{ User, TitleID, Kind, Name string }

// startSaveBackup queues backups of saves found by the last scan.
func (a *App) startSaveBackup(items []saveReq) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var jobs []*BuildJob
	for _, it := range items {
		for i := range a.saves.Saves {
			s := a.saves.Saves[i]
			if s.User == it.User && s.TitleID == it.TitleID && s.Kind == it.Kind {
				name := it.Name
				if name == "" {
					name = s.TitleID
				}
				jobs = append(jobs, &BuildJob{Key: "save:" + s.Path, Kind: "savebak", Name: name, Out: "save", State: "queued", Size: s.Size, save: &s})
			}
		}
	}
	if len(jobs) == 0 {
		return errors.New("Those saves aren't on the PS5 any more. Rescan and try again.")
	}
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	return nil
}

// startSaveDelete queues removing saves from the PS5. Each one is copied to
// this PC first, so a delete can be undone with Restore.
func (a *App) startSaveDelete(items []saveReq) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var jobs []*BuildJob
	for _, it := range items {
		for i := range a.saves.Saves {
			s := a.saves.Saves[i]
			if s.User == it.User && s.TitleID == it.TitleID && s.Kind == it.Kind {
				name := it.Name
				if name == "" {
					name = s.TitleID
				}
				jobs = append(jobs, &BuildJob{Key: "save:" + s.Path, Kind: "savedel", Name: name, Out: "save", State: "queued", Size: s.Size, save: &s})
			}
		}
	}
	if len(jobs) == 0 {
		return errors.New("Those saves aren't on the PS5 any more. Rescan and try again.")
	}
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	return nil
}

func (a *App) saveDelete(j *BuildJob) (string, error) {
	host, port, _ := a.ps5()
	s := *j.save
	// only ever a game's own save folder
	if !strings.HasPrefix(s.Path, "/user/home/"+s.User+"/savedata") || path.Base(s.Path) != s.TitleID || !titleIDRe.MatchString(s.TitleID) {
		return "", errors.New("that isn't a save folder, so it was left alone")
	}
	a.mu.Lock()
	j.State, j.Note = "deleting", "Keeping a copy on this PC first"
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer func() { go a.refreshSaves() }()
	cancelled := func() bool { return a.ps5Stopped(j) }
	if _, err := pullSave(f, host, s, j.Name, "delete", func(int64, int64) {}, cancelled); err != nil {
		if errors.Is(err, errFTPCancelled) {
			return "", err
		}
		return "", fmt.Errorf("the save couldn't be copied to this PC first (%v), so it was not deleted", err)
	}
	a.mu.Lock()
	j.Note = "Deleting the save from the PS5"
	a.mu.Unlock()
	if err := f.removeAll(s.Path, true, func() {}, cancelled); err != nil {
		return "", err
	}
	return "Save deleted from the PS5. A copy is kept in your backups.", nil
}

// startSaveRestore queues putting a backup back on the PS5.
func (a *App) startSaveRestore(id string) error {
	dir, err := backupDir(id)
	if err != nil {
		return err
	}
	var b SaveBackup
	raw, err := os.ReadFile(filepath.Join(dir, "loadout-save.json"))
	if err != nil || json.Unmarshal(raw, &b) != nil || !strings.HasPrefix(b.Path, "/user/home/") || !titleIDRe.MatchString(b.TitleID) {
		return errors.New("That backup is damaged, its details can't be read.")
	}
	b.ID = id
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	name := b.Name
	if name == "" {
		name = b.TitleID
	}
	j := &BuildJob{Key: "rest:" + id, Kind: "saverest", Name: name, Out: "save", State: "queued", Size: b.Size, backup: &b, image: dir}
	a.addJobsLocked([]*BuildJob{j})
	a.enqueuePS5Locked(j)
	return nil
}

func (a *App) saveProgress(j *BuildJob, state string) func(done, total int64) {
	var last time.Time
	return func(done, total int64) {
		if time.Since(last) < 150*time.Millisecond && done < total {
			return
		}
		last = time.Now()
		a.mu.Lock()
		j.State, j.Note = state, ""
		if total > 0 {
			j.Pct = int(done * 1000 / total)
		}
		a.mu.Unlock()
	}
}

func (a *App) saveBackup(j *BuildJob) (string, error) {
	host, port, _ := a.ps5()
	a.mu.Lock()
	j.State, j.Note = "saving", "Connecting to the PS5"
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer func() { go a.refreshSaves() }()
	b, err := pullSave(f, host, *j.save, j.Name, "", a.saveProgress(j, "saving"), func() bool { return a.ps5Stopped(j) })
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Save backed up (%s, %d files)", humanSize(b.Size), b.Files), nil
}

func (a *App) saveRestore(j *BuildJob) (string, error) {
	host, port, _ := a.ps5()
	b := j.backup
	a.mu.Lock()
	j.State, j.Note = "restoring", "Connecting to the PS5"
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer func() { go a.refreshSaves() }()
	cancelled := func() bool { return a.ps5Stopped(j) }
	// keep what's on the PS5 now, in case the restore turns out to be the wrong one
	kept := ""
	cur := PS5Save{User: b.User, UserName: b.UserName, TitleID: b.TitleID, Kind: b.Kind, Path: b.Path}
	a.mu.Lock()
	j.Note = "Keeping a copy of the save that's on the PS5 now"
	a.mu.Unlock()
	if _, err := pullSave(f, host, cur, j.Name, "restore", func(int64, int64) {}, cancelled); err == nil {
		kept = " The save that was on the PS5 is kept as a safety copy."
	} else if errors.Is(err, errFTPCancelled) {
		return "", err
	}
	var files []string
	var total, done int64
	root := filepath.Join(j.image, "files")
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			files = append(files, p)
			total += fi.Size()
		}
		return nil
	})
	if len(files) == 0 {
		return "", errors.New("that backup has no files in it")
	}
	prog := a.saveProgress(j, "restoring")
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		base := done
		_, err := f.uploadAs(p, path.Join(b.Path, path.Dir(rel)), path.Base(rel), true, func(sent, _ int64) {
			prog(base+sent, total)
		}, cancelled)
		if err != nil {
			if errors.Is(err, errFTPCancelled) {
				return "Stopped partway. Restore again so the save isn't left half written", err
			}
			return "", fmt.Errorf("%v. Restore again so the save isn't left half written", err)
		}
		fi, _ := os.Stat(p)
		done += fi.Size()
	}
	return fmt.Sprintf("Save restored (%d files).%s", len(files), kept), nil
}
