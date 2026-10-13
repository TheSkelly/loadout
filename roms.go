package main

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The ROM library: a folder on this PC with one subfolder per system, which
// the PS5 app browses and downloads from. Each game is a file, or a folder
// (for multi-file games), directly inside its system's folder:
//
//	ROMs\Switch\Some Game [0100...].nsp     -> ProsperoEden's roms folder
//	ROMs\PPSA99xxx\game.bin                 -> that emulator's games folder
//	ROMs\SNES\Some Game (USA).sfc            -> RetroArch content/SNES
//
// Nothing is copied or moved on this PC.

type romGame struct {
	Key    string `json:"key"` // path relative to the ROM folder, with forward slashes
	Name   string `json:"name"`
	File   string `json:"file"`
	System string `json:"system"`
	Emu    string `json:"emu"` // the emulator it goes to on the PS5
	Size   int64  `json:"size"`
	IsDir  bool   `json:"isDir"`
	path   string
}

type romSystem struct {
	Name  string    `json:"name"`
	Emu   string    `json:"emu"`
	Games []romGame `json:"games"`
}

var (
	romMu     sync.Mutex
	romCache  []romSystem
	romFrom   string
	romAt     time.Time
	romTags   = regexp.MustCompile(`\s*[\[(][^\])]*[\])]`)
	romIgnore = map[string]bool{".txt": true, ".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".xml": true, ".sfv": true,
		".md": true, ".url": true, ".ini": true, ".db": true, ".part": true, ".crdownload": true}
	switchNames = map[string]bool{"switch": true, "nsw": true, "ns": true, "nintendo switch": true, "prosperoeden": true, "eden": true}
)

const (
	edenID      = "PPSA99008"
	retroArchID = "PPSA99169"
)

// romEmu names the emulator a system folder's games go to.
func romEmu(system string) (id, name string) {
	s := strings.ToLower(strings.TrimSpace(system))
	if switchNames[s] {
		return edenID, knownEmus[edenID].Name
	}
	if id := strings.ToUpper(s); titleIDRe.MatchString(id) {
		if k, ok := knownEmus[id]; ok {
			return id, k.Name
		}
		return id, id
	}
	return retroArchID, knownEmus[retroArchID].Name
}

func romName(file string, isDir bool) string {
	n := file
	if !isDir {
		n = strings.TrimSuffix(n, filepath.Ext(n))
	}
	if c := strings.TrimSpace(romTags.ReplaceAllString(n, "")); c != "" {
		n = c
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(n, "_", " ")), " ")
}

// scanRoms lists the ROM folder; a scan is reused for a few seconds.
func scanRoms(root string) []romSystem {
	romMu.Lock()
	defer romMu.Unlock()
	if root == romFrom && time.Since(romAt) < 15*time.Second {
		return romCache
	}
	out := []romSystem{}
	systems, _ := os.ReadDir(root)
	for _, sd := range systems {
		if !sd.IsDir() || strings.HasPrefix(sd.Name(), ".") {
			continue
		}
		id, emu := romEmu(sd.Name())
		_ = id
		sys := romSystem{Name: sd.Name(), Emu: emu, Games: []romGame{}}
		ents, _ := os.ReadDir(filepath.Join(root, sd.Name()))
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, ".") || (!e.IsDir() && romIgnore[strings.ToLower(filepath.Ext(n))]) {
				continue
			}
			p := filepath.Join(root, sd.Name(), n)
			g := romGame{Key: sd.Name() + "/" + n, Name: romName(n, e.IsDir()), File: n, System: sd.Name(), Emu: emu, IsDir: e.IsDir(), path: p}
			if e.IsDir() {
				g.Size = folderSize(p)
			} else if fi, err := e.Info(); err == nil {
				g.Size = fi.Size()
			}
			sys.Games = append(sys.Games, g)
		}
		sort.Slice(sys.Games, func(i, j int) bool { return strings.ToLower(sys.Games[i].Name) < strings.ToLower(sys.Games[j].Name) })
		if len(sys.Games) > 0 {
			out = append(out, sys)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	romCache, romFrom, romAt = out, root, time.Now()
	return out
}

// romDest finds the folder on the PS5 a system's games go in.
func (a *App) romDest(system string) (string, error) {
	id, name := romEmu(system)
	if k, ok := knownEmus[id]; ok && k.Base != "" {
		return path.Join(k.Base, k.Games), nil
	}
	host, port, _ := a.ps5()
	if host == "" {
		return "", errors.New("Set your PS5's IP address in Loadout's Settings first.")
	}
	f, err := ftpDial(host, port)
	if err != nil {
		return "", errors.New("Your PC can't reach the PS5 to find " + name + ": " + shortNetErr(err))
	}
	defer f.Close()
	a.mu.Lock()
	roots := a.fxRootsLocked("homebrew")
	a.mu.Unlock()
	for _, r := range roots {
		if !strings.HasSuffix(r.Path, "/homebrew") {
			continue
		}
		app := path.Join(r.Path, id)
		if _, err := f.list(app); err != nil {
			continue
		}
		dir := a.emuGamesDir(f, id, app)
		if dir == "" {
			return "", errors.New("Loadout doesn't know where " + name + " keeps its games. Set it in Loadout's Emulators tab on the PC.")
		}
		if id == retroArchID {
			dir = path.Join(dir, system) // one folder per system keeps RetroArch tidy
		}
		return dir, nil
	}
	return "", errors.New(name + " isn't installed on the PS5. Install it from Loadout's Emulators tab on the PC.")
}

// startRomGet sends one game from the ROM library to its emulator on the PS5.
func (a *App) startRomGet(key string) (string, error) {
	a.mu.Lock()
	root := a.cfg.RomFolder
	a.mu.Unlock()
	if root == "" {
		return "", errors.New("Pick your ROM folder in Loadout on the PC first (Emulators › ROM library).")
	}
	var g *romGame
	for _, s := range scanRoms(root) {
		for i := range s.Games {
			if s.Games[i].Key == key {
				g = &s.Games[i]
			}
		}
	}
	if g == nil {
		return "", errors.New("That game isn't in your ROM folder any more.")
	}
	dest, err := a.romDest(g.System)
	if err != nil {
		return "", err
	}
	id, _ := romEmu(g.System)
	j := &BuildJob{Key: "rom:" + key, Name: g.Name + " to " + g.Emu, Out: g.File, State: "queued", Size: g.Size, image: g.path, Dest: dest}
	switch {
	case g.IsDir:
		j.Kind, j.Dest = "fxupdir", path.Join(dest, g.File)
	case knownEmus[id].UnzipGames && strings.EqualFold(filepath.Ext(g.File), ".zip"):
		j.Kind = "fxzipflat"
	default:
		j.Kind = "fxup"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.addJobsLocked([]*BuildJob{j})
	a.enqueuePS5Locked(j)
	return "Downloading " + g.Name + " to " + g.Emu + ".", nil
}
