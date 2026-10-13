package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A small file browser for the PS5's game folders: look around, copy files
// either way, rename, make folders, delete. It never leaves those folders.

type fxRoot struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// fxRootsLocked lists the folders the browser may open: the folders games
// are sent to ("games"), or the homebrew folders where emulators and other
// homebrew apps live ("homebrew").
func (a *App) fxRootsLocked(mode string) []fxRoot {
	var out []fxRoot
	seen := map[string]bool{}
	add := func(p, label string) {
		p = path.Clean(p)
		if p != "/" && !seen[p] {
			seen[p] = true
			out = append(out, fxRoot{p, label})
		}
	}
	if mode == "homebrew" {
		add("/data/homebrew", "Internal")
		for _, d := range a.ps5info.Drives {
			if d.Kind != "internal" {
				add(path.Join(d.Root, "homebrew"), d.Short)
			}
		}
		for id, k := range knownEmus {
			if k.Base != "" {
				_ = id
				add(k.Base, k.Name+" files")
			}
		}
		return out
	}
	// only the folders games are sent to, one per drive
	for _, d := range a.ps5info.Drives {
		if d.Dir != "" {
			add(d.Dir, d.Short)
		}
	}
	if d := strings.TrimSpace(a.cfg.PS5Dir); d != "" {
		add(d, "Send folder")
	}
	if len(out) == 0 {
		add("/data/etaHEN/games", "Internal")
	}
	return out
}

func (a *App) fxAllRootsLocked() []fxRoot {
	out := append(a.fxRootsLocked("games"), a.fxRootsLocked("homebrew")...)
	for _, b := range emuBases() {
		out = append(out, fxRoot{b, "Emulator data"})
	}
	return out
}

// fxAllowed cleans p and reports whether it is inside one of the game folders.
func (a *App) fxAllowed(p string) (string, bool) {
	if strings.ContainsAny(p, "\r\n") {
		return "", false
	}
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	a.mu.Lock()
	roots := a.fxAllRootsLocked()
	a.mu.Unlock()
	for _, r := range roots {
		if p == r.Path || strings.HasPrefix(p, r.Path+"/") {
			return p, true
		}
	}
	return p, false
}

func (a *App) fxIsRoot(p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.fxAllRootsLocked() {
		if r.Path == p {
			return true
		}
	}
	return false
}

var errFxOutside = errors.New("That's outside the PS5's game folders, so Loadout leaves it alone.")

func fxName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\\r\n") {
		return "", errors.New("That name can't be used.")
	}
	return n, nil
}

func (a *App) fxDial() (*ftpConn, error) {
	host, port, _ := a.ps5()
	if host == "" {
		return nil, errors.New("Set your PS5's IP address in Settings first.")
	}
	return ftpDial(host, port)
}

func (a *App) fxRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/fx/list", func(r *http.Request) (any, error) {
		var b struct{ Dir, Mode string }
		body(r, &b)
		a.mu.Lock()
		roots := a.fxRootsLocked(b.Mode)
		a.mu.Unlock()
		if b.Dir == "" {
			return map[string]any{"roots": roots, "dir": "", "entries": []ftpEntry{}}, nil
		}
		dir, ok := a.fxAllowed(b.Dir)
		if !ok {
			return nil, errFxOutside
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		ents, err := f.list(dir)
		if err != nil {
			return map[string]any{"roots": roots, "dir": dir, "entries": []ftpEntry{}, "missing": true}, nil
		}
		out := []ftpEntry{}
		for _, e := range ents {
			if e.Name != "." && e.Name != ".." {
				out = append(out, e)
			}
		}
		return map[string]any{"roots": roots, "dir": dir, "entries": out}, nil
	})
	api("/api/fx/mkdir", func(r *http.Request) (any, error) {
		var b struct{ Dir, Name string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		name, err := fxName(b.Name)
		if !ok {
			return nil, errFxOutside
		}
		if err != nil {
			return nil, err
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return nil, f.mkdirAll(path.Join(dir, name))
	})
	api("/api/fx/rename", func(r *http.Request) (any, error) {
		var b struct{ Path, Name string }
		body(r, &b)
		p, ok := a.fxAllowed(b.Path)
		name, err := fxName(b.Name)
		if !ok || a.fxIsRoot(p) {
			return nil, errFxOutside
		}
		if err != nil {
			return nil, err
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		to := path.Join(path.Dir(p), name)
		if _, exists := f.size(to); exists {
			return nil, errors.New("There's already something with that name here.")
		}
		return nil, f.rename(p, to)
	})
	api("/api/fx/delete", func(r *http.Request) (any, error) {
		var b struct {
			Path  string
			IsDir bool
		}
		body(r, &b)
		p, ok := a.fxAllowed(b.Path)
		if !ok || a.fxIsRoot(p) {
			return nil, errFxOutside
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if err := f.removeAll(p, b.IsDir, func() {}, func() bool { return false }); err != nil {
			return nil, err
		}
		go a.refreshPS5()
		return nil, nil
	})
	api("/api/fx/upload", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok {
			return nil, errFxOutside
		}
		local, err := pickFile("Pick a file to copy to the PS5", "All files", "*.*")
		if err != nil || local == "" {
			return map[string]any{"picked": false}, err
		}
		fi, err := os.Stat(local)
		if err != nil || fi.IsDir() {
			return nil, errors.New("That file couldn't be opened.")
		}
		a.mu.Lock()
		j := &BuildJob{Key: "fx:" + path.Join(dir, fi.Name()), Kind: "fxup", Name: fi.Name() + " to the PS5", Out: fi.Name(), State: "queued", Size: fi.Size(), image: local, Dest: dir}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"picked": true}, nil
	})
	api("/api/fx/uploaddir", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok {
			return nil, errFxOutside
		}
		local, err := pickFolder("Pick a folder to copy to the PS5", "")
		if err != nil || local == "" {
			return map[string]any{"picked": false}, err
		}
		name := filepath.Base(filepath.Clean(local))
		if _, err := fxName(name); err != nil || !dirExists(local) {
			return nil, errors.New("That folder can't be copied.")
		}
		size := folderSize(local)
		a.mu.Lock()
		j := &BuildJob{Key: "fx:" + path.Join(dir, name), Kind: "fxupdir", Name: name + " to the PS5", Out: name, State: "queued", Size: size, image: local, Dest: path.Join(dir, name)}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"picked": true}, nil
	})
	api("/api/fx/installzip", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok {
			return nil, errFxOutside
		}
		local, err := pickFile("Pick the emulator's ZIP file", "ZIP files", "*.zip")
		if err != nil || local == "" {
			return map[string]any{"picked": false}, err
		}
		fi, err := os.Stat(local)
		if err != nil || fi.IsDir() || !strings.EqualFold(filepath.Ext(local), ".zip") {
			return nil, errors.New("Pick a .zip file.")
		}
		a.mu.Lock()
		j := &BuildJob{Key: "fxzip:" + local, Kind: "fxzip", Name: fi.Name(), Out: fi.Name(), State: "queued", Size: fi.Size(), image: local, Dest: dir}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"picked": true}, nil
	})
	api("/api/emu/update", func(r *http.Request) (any, error) {
		var b struct{ Path, Name string }
		body(r, &b)
		p, ok := a.fxAllowed(b.Path)
		if !ok || gameID(path.Base(p)) != path.Base(p) {
			return nil, errFxOutside
		}
		local, err := pickFile("Pick the ZIP of the newer version", "ZIP files", "*.zip")
		if err != nil || local == "" {
			return map[string]any{"picked": false}, err
		}
		fi, err := os.Stat(local)
		if err != nil || fi.IsDir() || !strings.EqualFold(filepath.Ext(local), ".zip") {
			return nil, errors.New("Pick a .zip file.")
		}
		name := b.Name
		if name == "" {
			name = path.Base(p)
		}
		a.mu.Lock()
		j := &BuildJob{Key: "fxzip:" + local, Kind: "fxzip", Name: name, Out: fi.Name(), State: "queued", Size: fi.Size(), image: local, Dest: path.Dir(p), only: path.Base(p)}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"picked": true}, nil
	})
	api("/api/fx/download", func(r *http.Request) (any, error) {
		var b struct {
			Path string
			Size int64
		}
		body(r, &b)
		p, ok := a.fxAllowed(b.Path)
		if !ok {
			return nil, errFxOutside
		}
		folder, err := pickFolder("Where should the file go on this PC?", "")
		if err != nil || folder == "" {
			return map[string]any{"picked": false}, err
		}
		local := filepath.Join(folder, path.Base(p))
		if fileExists(local) {
			return nil, fmt.Errorf("%s is already in that folder. Pick another folder or move it first.", path.Base(p))
		}
		a.mu.Lock()
		j := &BuildJob{Key: "fx:" + p, Kind: "fxdown", Name: path.Base(p) + " to this PC", Out: path.Base(p), State: "queued", Size: b.Size, image: local, Dest: p}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"picked": true}, nil
	})
}

// fxCopy runs one copy job, either direction.
func (a *App) fxCopy(j *BuildJob) (string, error) {
	a.mu.Lock()
	j.State, j.Note = "copying", "Connecting to the PS5"
	a.mu.Unlock()
	cancelled := func() bool { return a.ps5Stopped(j) }
	prog := a.saveProgress(j, "copying")
	start := time.Now()
	// one big file (an emulator game, say) goes through PS5Upload when it can
	if fi, serr := os.Stat(j.image); j.Kind == "fxup" && serr == nil && fi.Size() >= 32<<20 && p5uAvailable() {
		host, _, _ := a.ps5()
		a.mu.Lock()
		plain := a.cfg.PlainFTP
		if !plain {
			j.Note = "Getting PS5Upload ready on the PS5"
		}
		a.mu.Unlock()
		if !plain && host != "" {
			started, perr := p5uSend(host, j.image, path.Join(j.Dest, filepath.Base(j.image)), prog, nil, cancelled)
			switch {
			case started && perr == nil:
				go a.refreshPS5()
				return "Copied to the PS5 in " + fmtDur(time.Since(start)) + ", through PS5Upload", nil
			case started || errors.Is(perr, errFTPCancelled):
				return "", perr
			}
			a.mu.Lock()
			j.Note = "Connecting to the PS5"
			a.mu.Unlock()
		}
	}
	f, err := a.fxDial()
	if err != nil {
		return "", err
	}
	defer f.Close()
	if j.Kind == "fxupdir" {
		sent, err := uploadTreeFiles(f, j.image, j.Dest, prog, cancelled)
		n := len(sent)
		if err != nil {
			if errors.Is(err, errFTPCancelled) {
				return "Stopped partway. Copy the folder again to finish it", err
			}
			return "", err
		}
		if strings.HasPrefix(j.Key, "fxdir:") {
			a.recordUpload(j.Dest, j.Out, sent)
		}
		go a.refreshPS5()
		return fmt.Sprintf("Copied %d files to the PS5 in %s", n, fmtDur(time.Since(start))), nil
	}
	if j.Kind == "fxzipflat" {
		// a ZIP of files for one folder (firmware, keys, a game): send what's inside
		a.mu.Lock()
		j.Note = "Unzipping"
		a.mu.Unlock()
		tmp, err := os.MkdirTemp("", "loadout-zip")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := unzipTo(j.image, tmp, cancelled); err != nil {
			return "", err
		}
		sent, err := uploadTreeFiles(f, zipInner(tmp), j.Dest, prog, cancelled)
		n := len(sent)
		if err != nil {
			if errors.Is(err, errFTPCancelled) {
				return "Stopped partway. Add the ZIP again to finish it", err
			}
			return "", err
		}
		a.recordUpload(j.Dest, strings.TrimSuffix(j.Out, filepath.Ext(j.Out)), sent)
		if n == 0 {
			return "", errors.New("that ZIP has no files in it")
		}
		return fmt.Sprintf("Unzipped and copied %d files to the PS5 in %s", n, fmtDur(time.Since(start))), nil
	}
	if j.Kind == "fxzip" {
		a.mu.Lock()
		j.Note = "Unzipping"
		a.mu.Unlock()
		tmp, err := os.MkdirTemp("", "loadout-zip")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := unzipTo(j.image, tmp, cancelled); err != nil {
			return "", err
		}
		apps := zipApps(tmp, strings.TrimSuffix(filepath.Base(j.image), filepath.Ext(j.image)))
		if len(apps) == 0 {
			return "", errors.New("that ZIP has no files in it")
		}
		if j.only != "" {
			// an update: the ZIP has to be this very app
			var keep []zipApp
			for _, app := range apps {
				id := app.name
				if b, err := os.ReadFile(filepath.Join(app.dir, "sce_sys", "param.json")); err == nil {
					var p struct {
						TitleID string `json:"titleId"`
					}
					if json.Unmarshal(b, &p) == nil && p.TitleID != "" {
						id = p.TitleID
					}
				}
				if strings.EqualFold(id, j.only) || strings.EqualFold(app.name, j.only) {
					keep = append(keep, zipApp{app.dir, j.only})
				}
			}
			if len(keep) != 1 {
				var seen []string
				for _, app := range apps {
					seen = append(seen, app.name)
				}
				return "", fmt.Errorf("that ZIP isn't an update for this app (it holds %s, not %s). Nothing was changed", strings.Join(seen, ", "), j.only)
			}
			apps = keep
		}
		var names []string
		total := 0
		for _, app := range apps {
			a.mu.Lock()
			j.Note = "Copying " + app.name
			a.mu.Unlock()
			n, err := uploadTree(f, app.dir, path.Join(j.Dest, app.name), prog, cancelled)
			if err != nil {
				if errors.Is(err, errFTPCancelled) {
					return "Stopped partway. Install the ZIP again to finish it", err
				}
				return "", err
			}
			total += n
			names = append(names, app.name)
		}
		go a.refreshPS5()
		if j.only != "" {
			return fmt.Sprintf("Updated %s (%d files) in %s. Your games and settings were left alone", j.Name, total, fmtDur(time.Since(start))), nil
		}
		return fmt.Sprintf("Installed %s (%d files) in %s", strings.Join(names, ", "), total, fmtDur(time.Since(start))), nil
	}
	if j.Kind == "fxup" {
		if _, err := f.uploadAs(j.image, j.Dest, filepath.Base(j.image), true, prog, cancelled); err != nil {
			return "", err
		}
		go a.refreshPS5()
		return "Copied to the PS5 in " + fmtDur(time.Since(start)), nil
	}
	tmp := j.image + ".part"
	var done int64
	err = f.download(j.Dest, tmp, func(n int64) {
		done += n
		total := j.Size
		if total < done {
			total = done
		}
		prog(done, total)
	}, cancelled)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, j.image); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return "Copied to " + filepath.Dir(j.image) + " in " + fmtDur(time.Since(start)), nil
}

// uploadTree copies a whole folder to the PS5, keeping its layout.
func uploadTree(f *ftpConn, local, dest string, prog func(done, total int64), cancelled func() bool) (int, error) {
	files, err := uploadTreeFiles(f, local, dest, prog, cancelled)
	return len(files), err
}

// uploadTreeFiles is uploadTree that also says which files it sent.
func uploadTreeFiles(f *ftpConn, local, dest string, prog func(done, total int64), cancelled func() bool) ([]string, error) {
	type item struct {
		local, rel string
		size       int64
	}
	var items []item
	var dirs []string
	var total, done int64
	filepath.Walk(local, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(local, p)
		rel = filepath.ToSlash(rel)
		if fi.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		items = append(items, item{p, rel, fi.Size()})
		total += fi.Size()
		return nil
	})
	for _, d := range dirs {
		if err := f.mkdirAll(path.Join(dest, d)); err != nil {
			return nil, err
		}
	}
	for _, it := range items {
		if cancelled() {
			return nil, errFTPCancelled
		}
		base := done
		if _, err := f.uploadAs(it.local, path.Join(dest, path.Dir(it.rel)), path.Base(it.rel), true, func(sent, _ int64) {
			prog(base+sent, total)
		}, cancelled); err != nil {
			if errors.Is(err, errFTPCancelled) {
				return nil, err
			}
			return nil, fmt.Errorf("%s: %v", it.rel, err)
		}
		done += it.size
	}
	var sent []string
	for _, it := range items {
		sent = append(sent, it.rel)
	}
	return sent, nil
}

// unzipTo extracts a ZIP into dir. Entries that would land outside dir are refused.
func unzipTo(zipPath, dir string, cancelled func() bool) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return errors.New("that file couldn't be opened as a ZIP")
	}
	defer zr.Close()
	root := filepath.Clean(dir) + string(os.PathSeparator)
	for _, zf := range zr.File {
		if cancelled() {
			return errFTPCancelled
		}
		name := filepath.FromSlash(strings.ReplaceAll(zf.Name, "\\", "/"))
		target := filepath.Join(dir, name)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), root) {
			return fmt.Errorf("the ZIP has an entry that points outside itself (%s), so it wasn't installed", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}
		if zf.Mode()&os.ModeSymlink != 0 {
			continue // links are skipped
		}
		os.MkdirAll(filepath.Dir(target), 0755)
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("couldn't read %s from the ZIP: %v", zf.Name, err)
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("couldn't unzip %s: %v (is there enough free space on this PC?)", zf.Name, err)
		}
	}
	return nil
}

type zipApp struct{ dir, name string }

// zipApps works out which folders in an unzipped download are the apps.
// Downloads are often wrapped in an extra folder, so it steps through
// single-folder levels, then takes every folder named like a title ID
// (PPSA99008). Failing that, the whole thing is one app.
func zipApps(dir, fallback string) []zipApp {
	cur, name := dir, fallback
	for depth := 0; depth < 4; depth++ {
		ents, _ := os.ReadDir(cur)
		var subs []os.DirEntry
		files := 0
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "__MACOSX") || e.Name() == ".DS_Store" {
				continue
			}
			if e.IsDir() {
				subs = append(subs, e)
			} else {
				files++
			}
		}
		var apps []zipApp
		for _, s := range subs {
			if titleIDRe.MatchString(s.Name()) {
				apps = append(apps, zipApp{filepath.Join(cur, s.Name()), s.Name()})
			}
		}
		if len(apps) > 0 {
			return apps
		}
		if files == 0 && len(subs) == 1 {
			cur, name = filepath.Join(cur, subs[0].Name()), subs[0].Name()
			continue
		}
		if files == 0 && len(subs) == 0 {
			return nil
		}
		break
	}
	if _, err := fxName(name); err != nil {
		name = "homebrew-app"
	}
	return []zipApp{{cur, name}}
}

// zipInner steps past wrapper folders in an unzipped download (a ZIP that
// holds one folder that holds the files) and returns where the files start.
func zipInner(dir string) string {
	cur := dir
	for depth := 0; depth < 4; depth++ {
		ents, _ := os.ReadDir(cur)
		var subs []string
		files := 0
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "__MACOSX") || e.Name() == ".DS_Store" {
				os.RemoveAll(filepath.Join(cur, e.Name()))
				continue
			}
			if e.IsDir() {
				subs = append(subs, e.Name())
			} else {
				files++
			}
		}
		if files == 0 && len(subs) == 1 {
			cur = filepath.Join(cur, subs[0])
			continue
		}
		break
	}
	return cur
}
