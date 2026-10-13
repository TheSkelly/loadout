package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Cheats for emulator games. ProsperoEden reads them from a "mods" folder
// beside "roms": mods/<title ID>/<mod name>/cheats/<build ID>.txt. Cheat
// collections for Switch emulators already come sorted by title ID, so
// Loadout only has to find the cheat files in a folder the user picks and lay
// them out that way. It doesn't include or download any cheats.

var (
	cheatID16 = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)
	buildID   = regexp.MustCompile(`^[0-9A-Fa-f]{16,64}$`)
	romID     = regexp.MustCompile(`[\[(]([0-9A-Fa-f]{16})[\])]`)
)

type cheatFile struct {
	title string // 16-character game ID, upper case
	rel   string // where it goes under mods/<title>/
	local string
}

// scanCheats finds the cheat files under root. skipped counts text files that
// look like cheats but sit under no game ID, so there's nowhere to put them.
func scanCheats(root string) (files []cheatFile, skipped int) {
	root = filepath.Clean(root)
	seen := 0
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > 400000 {
			return filepath.SkipAll
		}
		if fi.IsDir() || !strings.EqualFold(filepath.Ext(p), ".txt") || fi.Size() == 0 || fi.Size() > 4<<20 {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Dir(root), p) // keeps the picked folder's own name
		parts := strings.Split(filepath.ToSlash(rel), "/")
		name := parts[len(parts)-1]
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		dirs := parts[:len(parts)-1]
		ti := -1
		for i := len(dirs) - 1; i >= 0; i-- {
			if cheatID16.MatchString(dirs[i]) {
				ti = i
				break
			}
		}
		isBuild := buildID.MatchString(stem)
		isNamed := strings.HasPrefix(strings.ToLower(name), "cheat_")
		if !isBuild && !isNamed {
			return nil
		}
		if ti < 0 {
			skipped++
			return nil
		}
		title := strings.ToUpper(dirs[ti])
		after := dirs[ti+1:]
		var dst string
		switch {
		case isNamed && len(after) == 0:
			dst = name // a single cheats file straight in the game's folder
		case len(after) >= 2 && strings.EqualFold(after[len(after)-1], "cheats"):
			dst = path.Join(after[len(after)-2], "cheats", name) // keeps the collection's own mod name
		default:
			dst = path.Join("Cheats", "cheats", name)
		}
		files = append(files, cheatFile{title, dst, p})
		return nil
	})
	return files, skipped
}

func cheatTitles(files []cheatFile, only map[string]bool) (titles, count int) {
	set := map[string]bool{}
	for _, f := range files {
		if only != nil && !only[f.title] {
			continue
		}
		set[f.title] = true
		count++
	}
	return len(set), count
}

// cheatWanted works out which game IDs the user's own games have: IDs in the
// game file names, plus game folders already present under mods (the emulator
// makes those from its Mods screen).
func cheatWanted(f *ftpConn, modsDir string) map[string]bool {
	want := map[string]bool{}
	for id := range cheatFolders(f, modsDir) {
		want[id] = true
	}
	for _, d := range []string{"roms", "updates"} {
		if ents, err := f.list(path.Join(path.Dir(modsDir), d)); err == nil {
			for _, e := range ents {
				if m := romID.FindStringSubmatch(e.Name); m != nil {
					id := strings.ToUpper(m[1])
					want[id] = true
					// an update's ID ends in 800; its game's ends in 000
					if strings.HasSuffix(id, "800") {
						want[id[:13]+"000"] = true
					}
				}
			}
		}
	}
	return want
}

// cheatFolders lists the game folders already under mods, by upper-case ID,
// with each one's name exactly as it is on the PS5.
func cheatFolders(f *ftpConn, modsDir string) map[string]string {
	have := map[string]string{}
	if ents, err := f.list(modsDir); err == nil {
		for _, e := range ents {
			if e.IsDir && cheatID16.MatchString(e.Name) {
				have[strings.ToUpper(e.Name)] = e.Name
			}
		}
	}
	return have
}

func (a *App) cheatRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/emu/cheats/pick", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok || path.Base(dir) != "mods" {
			return nil, errFxOutside
		}
		local, err := pickFolder("Pick your cheats folder", "")
		if err != nil || local == "" {
			return map[string]any{"path": ""}, err
		}
		files, skipped := scanCheats(local)
		titles, count := cheatTitles(files, nil)
		res := map[string]any{"path": local, "titles": titles, "files": count, "skipped": skipped, "mine": 0, "mineFiles": 0}
		if count > 0 {
			if f, err := a.fxDial(); err == nil {
				want := cheatWanted(f, dir)
				f.Close()
				res["mine"], res["mineFiles"] = cheatTitles(files, want)
			}
		}
		return res, nil
	})
	api("/api/emu/cheats/send", func(r *http.Request) (any, error) {
		var b struct {
			Dir, Path string
			Only      bool
		}
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok || path.Base(dir) != "mods" {
			return nil, errFxOutside
		}
		if !dirExists(b.Path) {
			return nil, errors.New("That folder isn't there any more.")
		}
		files, _ := scanCheats(b.Path)
		var only map[string]bool
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		if b.Only {
			only = cheatWanted(f, dir)
		}
		// a game folder the emulator already made keeps its exact name, so the
		// cheats land inside it instead of in a second folder beside it
		existing := cheatFolders(f, dir)
		f.Close()
		titles, count := cheatTitles(files, only)
		if count == 0 {
			return nil, errors.New("There are no cheat files to send from that folder.")
		}
		// lay the files out the way the emulator wants, then send that as one folder
		if olds, _ := filepath.Glob(filepath.Join(os.TempDir(), "loadout-cheats-*")); len(olds) > 0 {
			for _, o := range olds {
				if fi, err := os.Stat(o); err == nil && time.Since(fi.ModTime()) > 6*time.Hour {
					os.RemoveAll(o)
				}
			}
		}
		tmp, err := os.MkdirTemp("", "loadout-cheats-")
		if err != nil {
			return nil, err
		}
		stage := filepath.Join(tmp, "mods")
		for _, cf := range files {
			if only != nil && !only[cf.title] {
				continue
			}
			folder := cf.title
			if n := existing[cf.title]; n != "" {
				folder = n
			}
			dst := filepath.Join(stage, folder, filepath.FromSlash(cf.rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return nil, err
			}
			in, err := os.Open(cf.local)
			if err != nil {
				continue
			}
			out, err := os.Create(dst)
			if err == nil {
				io.Copy(out, in)
				out.Close()
			}
			in.Close()
		}
		name := fmt.Sprintf("Cheats for %d game", titles)
		if titles != 1 {
			name += "s"
		}
		a.mu.Lock()
		j := &BuildJob{Key: "fx:" + dir, Kind: "fxupdir", Name: name, Out: "mods", State: "queued", Size: folderSize(stage), image: stage, Dest: dir}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"titles": titles, "files": count}, nil
	})
}

// ---- what's installed ----

type cheatSet struct {
	File   string   `json:"file"`   // the build ID the file is named after
	Cheats []string `json:"cheats"` // cheat names, in the file's order
}

type modInfo struct {
	Name    string     `json:"name"`
	Sets    []cheatSet `json:"sets"`
	Patches int        `json:"patches"` // code patches (exefs)
	Files   bool       `json:"files"`   // replacement game files (romfs)
}

var cheatNameRe = regexp.MustCompile(`(?m)^\s*[\[{]\s*([^\]}\r\n]+?)\s*[\]}]\s*$`)

// Cheat files made on a Switch use the console's own button symbols in cheat
// names (a cheat that only runs while ZL is held, say). No PC font has those
// symbols, so they show as empty boxes. These are the ones worth naming.
var switchButtons = map[rune]string{
	0xE0A0: "A", 0xE0A1: "B", 0xE0A2: "X", 0xE0A3: "Y", 0xE0A4: "L", 0xE0A5: "R", 0xE0A6: "ZL", 0xE0A7: "ZR",
	0xE0A8: "SL", 0xE0A9: "SR", 0xE0AA: "D-pad", 0xE0AB: "Up", 0xE0AC: "Down", 0xE0AD: "Left", 0xE0AE: "Right",
	0xE0AF: "Up", 0xE0B0: "Down", 0xE0B1: "Left", 0xE0B2: "Right", 0xE0B3: "+", 0xE0B4: "-",
	0xE0C0: "Stick", 0xE0C1: "Left stick", 0xE0C2: "Right stick", 0xE0C3: "Stick press", 0xE0C4: "Left stick press", 0xE0C5: "Right stick press",
	0xE0E0: "A", 0xE0E1: "B", 0xE0E2: "X", 0xE0E3: "Y", 0xE0E4: "L", 0xE0E5: "R", 0xE0E6: "ZL", 0xE0E7: "ZR",
	0xE0E8: "SL", 0xE0E9: "SR", 0xE0EA: "D-pad", 0xE0EB: "Up", 0xE0EC: "Down", 0xE0ED: "Left", 0xE0EE: "Right",
	0xE0EF: "+", 0xE0F0: "-", 0xE100: "Left stick", 0xE101: "Right stick", 0xE104: "Left stick press", 0xE105: "Right stick press",
}

var cheatHeaderRe = regexp.MustCompile(`(?i)\b[TB]ID\s*[:=]`)

// cleanCheatName tidies one cheat name: button symbols become words after the
// name, other unshowable characters go. ok is false for a line that is the
// file's own header (which game and build it is for), not a cheat.
func cleanCheatName(s string) (name string, ok bool) {
	if cheatHeaderRe.MatchString(s) {
		return "", false
	}
	var out []rune
	var keys []string
	for _, r := range s {
		switch {
		case switchButtons[r] != "":
			keys = append(keys, switchButtons[r])
		case r >= 0xE000 && r <= 0xF8FF, r >= 0xF0000, r == 0xFFFD, r < 0x20, r == 0x7F, r >= 0xFE00 && r <= 0xFE0F, r == 0x200B, r == 0x200D:
			// a symbol only the console can draw, or an invisible character
		default:
			out = append(out, r)
		}
	}
	name = strings.Join(strings.Fields(string(out)), " ")
	name = strings.Trim(name, " -:+")
	if len(keys) > 0 {
		hold := strings.Join(keys, " + ")
		if name == "" {
			name = "Hold " + hold
		} else {
			name += " (hold " + hold + ")"
		}
	}
	return name, name != ""
}

func cheatNames(b []byte) []string {
	out := []string{}
	for _, m := range cheatNameRe.FindAllStringSubmatch(strings.ToValidUTF8(string(b), ""), 400) {
		if n, ok := cleanCheatName(m[1]); ok {
			out = append(out, n)
		}
	}
	return out
}

var (
	breezeLead = regexp.MustCompile(`(?i)^\s*(breeze|edizon(\s*se)?)\s+\S+\s+`)
	verTail    = regexp.MustCompile(`(?i)\s+v?\d+(\.\d+)+\s*$`)
)

// cheatFileGame reads the game's name out of a cheat file's header line,
// which cheat tools write as "[Breeze beta84 Game Name 1.0.0 TID: ... BID: ...]".
func cheatFileGame(b []byte) string {
	for _, m := range cheatNameRe.FindAllStringSubmatch(strings.ToValidUTF8(string(b), ""), 6) {
		loc := cheatHeaderRe.FindStringIndex(m[1])
		if loc == nil {
			continue
		}
		n := strings.TrimSpace(m[1][:loc[0]])
		n = breezeLead.ReplaceAllString(n, "")
		n = verTail.ReplaceAllString(n, "")
		if n, ok := cleanCheatName(n); ok && len(n) >= 2 {
			return n
		}
	}
	return ""
}

// modTitles finds a game name for each game ID folder under mods: from the
// user's own game files, else from the header of a cheat file inside. Names
// are remembered so later visits are quick.
func (a *App) modTitles(f *ftpConn, modsDir string, folders []string) map[string]string {
	known := map[string]string{}
	loadJSON("modnames.json", &known)
	out := map[string]string{}
	romsDir := path.Join(path.Dir(modsDir), "roms")
	fromRoms := map[string]string{}
	if ents, err := f.list(romsDir); err == nil {
		for _, e := range ents {
			id := nameID(e.Name)
			if id == "" {
				continue
			}
			if b, err := os.ReadFile(emuCoverPath(emuCoverKey(romsDir, e.Name)) + ".name"); err == nil && len(b) > 0 {
				fromRoms[id] = strings.TrimSpace(string(b))
			} else if stem := strings.TrimSpace(idInName.ReplaceAllString(strings.TrimSuffix(e.Name, path.Ext(e.Name)), "")); stem != "" {
				fromRoms[id] = stem
			}
		}
	}
	lookups, changed := 0, false
	for _, folder := range folders {
		id := strings.ToUpper(folder)
		if !cheatID16.MatchString(folder) {
			continue
		}
		if n := fromRoms[id]; n != "" {
			out[folder] = n
			continue
		}
		if n, ok := known[id]; ok {
			if n != "" {
				out[folder] = n
			}
			continue
		}
		if lookups++; lookups > 120 {
			continue // a huge collection: the rest get their names on later visits
		}
		name := ""
		root := path.Join(modsDir, folder)
		if ents, err := f.list(root); err == nil {
		find:
			for _, e := range ents {
				if !e.IsDir {
					if strings.HasSuffix(strings.ToLower(e.Name), ".txt") {
						if b, err := f.get(path.Join(root, e.Name), 4096); err == nil {
							if name = cheatFileGame(b); name != "" {
								break find
							}
						}
					}
					continue
				}
				cs, err := f.list(path.Join(root, e.Name, "cheats"))
				if err != nil {
					continue
				}
				for i, c := range cs {
					if c.IsDir || i > 3 {
						continue
					}
					if b, err := f.get(path.Join(root, e.Name, "cheats", c.Name), 4096); err == nil {
						if name = cheatFileGame(b); name != "" {
							break find
						}
					}
				}
			}
		}
		known[id], changed = name, true
		if name != "" {
			out[folder] = name
		}
	}
	if changed {
		saveJSON("modnames.json", known)
	}
	return out
}

// cheatSummary counts the cheat files under each wanted game's mods folder.
func cheatSummary(f *ftpConn, modsDir string, ids []string) map[string]int {
	out := map[string]int{}
	have := cheatFolders(f, modsDir)
	for _, id := range ids {
		id = strings.ToUpper(id)
		folder := have[id]
		if folder == "" {
			continue
		}
		n := 0
		ents, err := f.list(path.Join(modsDir, folder))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir {
				if strings.HasPrefix(strings.ToLower(e.Name), "cheat_") && strings.HasSuffix(strings.ToLower(e.Name), ".txt") {
					n++
				}
				continue
			}
			subs, err := f.list(path.Join(modsDir, folder, e.Name))
			if err != nil {
				continue
			}
			for _, s := range subs {
				if !s.IsDir {
					continue
				}
				switch strings.ToLower(s.Name) {
				case "cheats":
					if cs, err := f.list(path.Join(modsDir, folder, e.Name, s.Name)); err == nil {
						for _, c := range cs {
							if !c.IsDir && strings.HasSuffix(strings.ToLower(c.Name), ".txt") {
								n++
							}
						}
					}
				case "exefs", "romfs":
					n++ // a mod without cheats still counts as something installed
				}
			}
		}
		if n > 0 {
			out[id] = n
		}
	}
	return out
}

// cheatDetail reads what one game has installed: each mod, and the cheats in its files.
func cheatDetail(f *ftpConn, modsDir, id string) []modInfo {
	out := []modInfo{}
	folder := cheatFolders(f, modsDir)[strings.ToUpper(id)]
	if folder == "" {
		return out
	}
	root := path.Join(modsDir, folder)
	ents, err := f.list(root)
	if err != nil {
		return out
	}
	for _, e := range ents {
		low := strings.ToLower(e.Name)
		if !e.IsDir {
			if strings.HasPrefix(low, "cheat_") && strings.HasSuffix(low, ".txt") && e.Size <= 1<<20 {
				if b, err := f.get(path.Join(root, e.Name), 1<<20); err == nil {
					out = append(out, modInfo{Name: strings.TrimSuffix(e.Name[6:], path.Ext(e.Name)), Sets: []cheatSet{{File: "", Cheats: cheatNames(b)}}})
				}
			}
			continue
		}
		m := modInfo{Name: e.Name, Sets: []cheatSet{}}
		subs, err := f.list(path.Join(root, e.Name))
		if err != nil {
			continue
		}
		for _, s := range subs {
			if !s.IsDir {
				continue
			}
			switch strings.ToLower(s.Name) {
			case "cheats":
				cs, _ := f.list(path.Join(root, e.Name, s.Name))
				for i, c := range cs {
					if c.IsDir || !strings.HasSuffix(strings.ToLower(c.Name), ".txt") || c.Size > 1<<20 || i > 60 {
						continue
					}
					if b, err := f.get(path.Join(root, e.Name, s.Name, c.Name), 1<<20); err == nil {
						m.Sets = append(m.Sets, cheatSet{File: strings.TrimSuffix(c.Name, path.Ext(c.Name)), Cheats: cheatNames(b)})
					}
				}
			case "exefs":
				if ps, err := f.list(path.Join(root, e.Name, s.Name)); err == nil {
					m.Patches = len(ps)
				}
			case "romfs":
				m.Files = true
			}
		}
		if len(m.Sets) > 0 || m.Patches > 0 || m.Files {
			out = append(out, m)
		}
	}
	return out
}

func (a *App) cheatViewRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/emu/cheats/summary", func(r *http.Request) (any, error) {
		var b struct {
			Dir string
			IDs []string
		}
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok || path.Base(dir) != "mods" {
			return nil, errFxOutside
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return cheatSummary(f, dir, b.IDs), nil
	})
	api("/api/emu/cheats/show", func(r *http.Request) (any, error) {
		var b struct{ Dir, ID string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok || path.Base(dir) != "mods" || !cheatID16.MatchString(b.ID) {
			return nil, errFxOutside
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return cheatDetail(f, dir, b.ID), nil
	})
}
