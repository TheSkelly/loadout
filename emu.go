package main

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Emulators: homebrew apps in the PS5's homebrew folders, each with a folder
// its games go in.

type emuSlotDef struct {
	Key, Label, Dir, Add, Hint string
	Unzip                      bool // a ZIP picked here is opened and its contents sent
	Group                      bool // show one line per upload instead of every file
}

type emuKnown struct {
	Name, System string
	// Base is where the app keeps its files when that is outside its own
	// folder; "" means inside the app folder.
	Base  string
	Games string
	// UnzipGames: a game picked as a ZIP is opened first. Off for emulators
	// that read zipped games as they are (arcade sets must stay zipped).
	UnzipGames bool
	// Covers is the folder (under Base) where the app caches game covers.
	Covers string
	// Extra are other kinds of files the app needs, each with its own folder.
	Extra []emuSlotDef
}

// knownEmus are apps whose layout is documented by their own projects.
var knownEmus = map[string]emuKnown{
	"PPSA99008": {Name: "ProsperoEden", System: "Nintendo Switch", Base: "/data/prosperoeden", Games: "roms", UnzipGames: true, Covers: "covers", Extra: []emuSlotDef{
		{"keys", "Keys", "keys", "Add key files", "prod.keys, and title.keys if you have it", true, false},
		{"firmware", "Firmware", "firmware", "Add firmware files", "the .nca files. Pick the firmware ZIP and Loadout unzips it, or use Add a folder if it is already unzipped", true, true},
		{"updates", "Updates & DLC", "updates", "Add update or DLC", "optional: update and DLC files for your games", true, false},
		{"mods", "Cheats & mods", "mods", "Add a cheats folder", "optional: cheats and mods, one folder per game ID. Pick your cheats folder and Loadout sorts it into place. Switch cheats on in ProsperoEden: Triangle on the game, then Mods", false, false},
	}},
	"PPSA99169": {Name: "RetroArch", System: "Classic consoles and arcade", Games: "content", Extra: []emuSlotDef{
		{"system", "System files", "system", "Add system files", "BIOS and other files some cores need", false, false},
	}},
}

// emuBases are the extra folders (outside the homebrew folders) that known
// apps keep their files in. The file tools may work inside these too.
func emuBases() []string {
	var out []string
	for _, k := range knownEmus {
		if k.Base != "" {
			out = append(out, k.Base)
		}
	}
	sort.Strings(out)
	return out
}

type EmuSlot struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Dir   string `json:"dir"`
	Add   string `json:"add"`
	Hint  string `json:"hint"`
	Unzip bool   `json:"unzip"`
	Count int    `json:"count"`
	Size  int64  `json:"size"`
}

// gameDirGuesses are tried, in order, for apps Loadout doesn't know.
var gameDirGuesses = []string{"assets/roms", "roms", "content", "games", "ROMS", "Roms"}

type EmuApp struct {
	Folder   string    `json:"folder"` // folder name, usually a title ID
	Path     string    `json:"path"`
	Drive    string    `json:"drive"`
	Name     string    `json:"name"`
	System   string    `json:"system"`
	GamesDir string    `json:"gamesDir"` // full path, "" when unknown
	Games    int       `json:"games"`
	Size     int64     `json:"size"`
	Slots    []EmuSlot `json:"slots"` // games first, then anything else the app needs
	Icon     string    `json:"icon"`  // title ID whose icon was fetched, "" when none
}

func isGameFile(e ftpEntry) bool {
	n := strings.ToLower(e.Name)
	return !strings.HasPrefix(n, ".") && !strings.HasSuffix(n, ".part") && n != "readme.txt"
}

// emuGamesDir works out where an app keeps its games.
func (a *App) emuGamesDir(f *ftpConn, folder, appPath string) string {
	a.mu.Lock()
	custom := a.cfg.EmuDirs[folder]
	a.mu.Unlock()
	if custom != "" {
		return path.Join(appPath, custom)
	}
	if k, ok := knownEmus[strings.ToUpper(folder)]; ok {
		if k.Base != "" {
			return path.Join(k.Base, k.Games)
		}
		return path.Join(appPath, k.Games)
	}
	for _, g := range gameDirGuesses {
		if _, err := f.list(path.Join(appPath, g)); err == nil {
			return path.Join(appPath, g)
		}
	}
	return ""
}

func (a *App) emuList(f *ftpConn) []EmuApp {
	a.mu.Lock()
	roots := a.fxRootsLocked("homebrew")
	a.mu.Unlock()
	out := []EmuApp{}
	for _, r := range roots {
		if !strings.HasSuffix(r.Path, "/homebrew") {
			continue // an emulator's data folder, not a place apps are installed
		}
		ents, err := f.list(r.Path)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir || strings.HasPrefix(e.Name, ".") {
				continue
			}
			// "Some Game [PPSA01234] v01.000" is a game folder that happens to sit here, not an app
			if id := gameID(e.Name); id != "" && !strings.EqualFold(id, e.Name) && !strings.EqualFold(id+"-app", e.Name) {
				continue
			}
			app := EmuApp{Folder: e.Name, Path: path.Join(r.Path, e.Name), Drive: r.Label, Name: e.Name}
			if k, ok := knownEmus[strings.ToUpper(e.Name)]; ok {
				app.Name, app.System = k.Name, k.System
			}
			if id := gameID(e.Name); id != "" {
				fetchIcon(f, id, path.Join(app.Path, "sce_sys", "icon0.png"))
				if fileExists(ps5IconPath(id)) {
					app.Icon = id
				}
			}
			app.GamesDir = a.emuGamesDir(f, e.Name, app.Path)
			count := func(dir string) (n int, size int64) {
				if gs, err := f.list(dir); err == nil {
					for _, g := range gs {
						if isGameFile(g) {
							n++
							size += g.Size
						}
					}
				}
				return
			}
			if app.GamesDir != "" {
				app.Games, app.Size = count(app.GamesDir)
				app.Slots = append(app.Slots, EmuSlot{Key: "games", Label: "Games", Dir: app.GamesDir, Add: "Add game", Count: app.Games, Size: app.Size, Unzip: knownEmus[strings.ToUpper(e.Name)].UnzipGames})
			}
			if k, ok := knownEmus[strings.ToUpper(e.Name)]; ok {
				base := app.Path
				if k.Base != "" {
					base = k.Base
				}
				for _, x := range k.Extra {
					s := EmuSlot{Key: x.Key, Label: x.Label, Dir: path.Join(base, x.Dir), Add: x.Add, Hint: x.Hint, Unzip: x.Unzip}
					s.Count, s.Size = count(s.Dir)
					app.Slots = append(app.Slots, s)
				}
			}
			if app.Slots == nil {
				app.Slots = []EmuSlot{}
			}
			out = append(out, app)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (a *App) emuRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/emu/list", func(r *http.Request) (any, error) {
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return map[string]any{"apps": a.emuList(f)}, nil
	})
	api("/api/emu/games", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
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
			return map[string]any{"games": []ftpEntry{}, "missing": true}, nil
		}
		if groupedDir(dir) {
			return map[string]any{"games": []ftpEntry{}, "groups": a.groupsFor(dir, ents)}, nil
		}
		type game struct {
			ftpEntry
			Cover string `json:"cover"`
			Title string `json:"title"`
		}
		games := []game{}
		for _, e := range ents {
			if isGameFile(e) {
				games = append(games, game{ftpEntry: e})
			}
		}
		// thumbnails: the emulator's own cached covers, when it keeps any
		if cdir := emuCoversDir(dir); cdir != "" {
			var names []string
			for _, g := range games {
				names = append(names, g.Name)
			}
			found, titles := edenCovers(f, cdir, dir, names)
			for i := range games {
				games[i].Cover, games[i].Title = found[games[i].Name], titles[games[i].Name]
			}
		}
		// the cheats tab lists game ID folders: show which game each one is
		if path.Base(dir) == "mods" {
			var folders []string
			for _, g := range games {
				if g.IsDir {
					folders = append(folders, g.Name)
				}
			}
			titles := a.modTitles(f, dir, folders)
			for i := range games {
				games[i].Title = titles[games[i].Name]
			}
		}
		return map[string]any{"games": games}, nil
	})
	// remove everything one upload brought (a whole firmware, say)
	api("/api/emu/removegroup", func(r *http.Request) (any, error) {
		var b struct{ Dir, Name string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok || !groupedDir(dir) {
			return nil, errFxOutside
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		ents, err := f.list(dir)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, g := range a.groupsFor(dir, ents) {
			if g.Name != b.Name {
				continue
			}
			for _, fl := range g.Files {
				if _, err := fxName(fl.Name); err != nil {
					continue
				}
				if err := f.removeAll(path.Join(dir, fl.Name), false, func() {}, func() bool { return false }); err != nil {
					return nil, err
				}
				n++
			}
		}
		a.mu.Lock()
		gs := a.uploads[dir]
		kept := gs[:0]
		for _, g := range gs {
			if g.Name != b.Name {
				kept = append(kept, g)
			}
		}
		a.uploads[dir] = kept
		saveJSON("emu-uploads.json", a.uploads)
		a.mu.Unlock()
		return map[string]any{"removed": n}, nil
	})
	// set (or clear) the games folder for an app Loadout doesn't know
	api("/api/emu/setdir", func(r *http.Request) (any, error) {
		var b struct{ Folder, AppPath, Dir string }
		body(r, &b)
		appPath, ok := a.fxAllowed(b.AppPath)
		dir, ok2 := a.fxAllowed(b.Dir)
		if !ok || !ok2 || (dir != appPath && !strings.HasPrefix(dir, appPath+"/")) {
			return nil, errors.New("Pick a folder inside this emulator's own folder.")
		}
		a.mu.Lock()
		if a.cfg.EmuDirs == nil {
			a.cfg.EmuDirs = map[string]string{}
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(dir, appPath), "/")
		if rel == "" {
			rel = "."
		}
		a.cfg.EmuDirs[b.Folder] = rel
		saveJSON("settings.json", a.cfg)
		a.mu.Unlock()
		return nil, nil
	})
	// add games: one file, or every file in a folder
	api("/api/emu/add", func(r *http.Request) (any, error) {
		var b struct {
			Dir    string
			Folder bool
			Unzip  bool // open a picked ZIP and send what's inside
		}
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok {
			return nil, errFxOutside
		}
		var locals []string
		if b.Folder {
			src, err := pickFolder("Pick the folder with the files to add", "")
			if err != nil || src == "" {
				return map[string]any{"picked": 0}, err
			}
			n := 0
			filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					n++
				}
				return nil
			})
			if n == 0 {
				return nil, errors.New("That folder has no files in it.")
			}
			// one job: everything in the folder lands in the emulator's folder, layout kept
			a.mu.Lock()
			j := &BuildJob{Key: "fxdir:" + dir, Kind: "fxupdir", Name: fmt.Sprintf("%d files to the PS5", n), Out: filepath.Base(src), State: "queued", Size: folderSize(src), image: src, Dest: dir}
			a.addJobsLocked([]*BuildJob{j})
			a.enqueuePS5Locked(j)
			a.mu.Unlock()
			return map[string]any{"picked": n}, nil
		} else {
			local, err := pickFile("Pick a game to add", "All files", "*.*")
			if err != nil || local == "" {
				return map[string]any{"picked": 0}, err
			}
			locals = []string{local}
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		var jobs []*BuildJob
		for _, local := range locals {
			fi, err := os.Stat(local)
			if err != nil || fi.IsDir() {
				continue
			}
			if b.Unzip && strings.EqualFold(filepath.Ext(local), ".zip") {
				jobs = append(jobs, &BuildJob{Key: "fxdir:" + dir, Kind: "fxzipflat", Name: fi.Name(), Out: fi.Name(), State: "queued", Size: fi.Size(), image: local, Dest: dir})
				continue
			}
			jobs = append(jobs, &BuildJob{Key: "fx:" + path.Join(dir, fi.Name()), Kind: "fxup", Name: fi.Name() + " to the PS5", Out: fi.Name(), State: "queued", Size: fi.Size(), image: local, Dest: dir})
		}
		if len(jobs) == 0 {
			return nil, errors.New("That file couldn't be opened.")
		}
		a.addJobsLocked(jobs)
		for _, j := range jobs {
			a.enqueuePS5Locked(j)
		}
		return map[string]any{"picked": len(jobs)}, nil
	})
}

// emuCoversDir returns the covers folder for a known emulator's games folder.
func emuCoversDir(gamesDir string) string {
	for _, k := range knownEmus {
		if k.Covers != "" && k.Base != "" && path.Join(k.Base, k.Games) == gamesDir {
			return path.Join(k.Base, k.Covers)
		}
	}
	return ""
}

var hex16 = regexp.MustCompile(`(?i)[0-9a-f]{16}`)
var bracketed = regexp.MustCompile(`[\[(][^\])]*[\])]`)

func plainKey(s string) string {
	return nonWord.ReplaceAllString(strings.ToLower(s), "")
}

func emuCoverPath(key string) string {
	return filepath.Join(appDir(), "emucovers", key+".img")
}

func emuCoverKey(gamesDir, name string) string {
	h := sha1.Sum([]byte(gamesDir + "/" + name))
	return hex.EncodeToString(h[:10])
}
