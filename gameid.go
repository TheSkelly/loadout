package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Switch games are known to the emulator by a 16-character title ID. Cheats,
// mods and save imports are all filed under it, so having it in the game's
// file name ("Game [0100ABCDEF012345].nsp") lets Loadout match things up.
// Loadout can read the ID out of an NSP that carries a ticket (the ticket's
// file name starts with it; nothing is decrypted). For other files it offers
// the IDs it can see on the PS5, or the user types it.

var (
	idInName   = regexp.MustCompile(`\s*[\[(]([0-9A-Fa-f]{16})[\])]`)
	idAnywhere = regexp.MustCompile(`"(01[0-9A-Fa-f]{14})"`)
	titleIDFmt = regexp.MustCompile(`^01[0-9A-Fa-f]{14}$`)
)

func nameID(name string) string {
	if m := idInName.FindStringSubmatch(name); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// baseTitle turns an update's ID (ends in 800) into its game's (ends in 000).
func baseTitle(id string) string {
	id = strings.ToUpper(id)
	if len(id) == 16 && strings.HasSuffix(id, "800") {
		return id[:13] + "000"
	}
	return id
}

// nspTitleID reads the start of an NSP on the PS5 and takes the game ID from
// the name of the ticket file listed inside it.
func nspTitleID(f *ftpConn, p string) string {
	b, err := f.get(p, 1<<20)
	if err != nil && len(b) < 16 {
		return ""
	}
	if len(b) < 16 || string(b[:4]) != "PFS0" {
		return ""
	}
	n := int(binary.LittleEndian.Uint32(b[4:8]))
	strSize := int(binary.LittleEndian.Uint32(b[8:12]))
	if n <= 0 || n > 8192 || strSize <= 0 || 16+24*n+strSize > len(b) {
		return ""
	}
	strs := b[16+24*n : 16+24*n+strSize]
	for i := 0; i < n; i++ {
		off := int(binary.LittleEndian.Uint32(b[16+24*i+16:]))
		if off >= len(strs) {
			continue
		}
		end := off
		for end < len(strs) && strs[end] != 0 {
			end++
		}
		name := string(strs[off:end])
		if strings.HasSuffix(strings.ToLower(name), ".tik") && len(name) >= 20 && titleIDFmt.MatchString(name[:16]) {
			return baseTitle(name[:16])
		}
	}
	return ""
}

// knownTitleIDs gathers the game IDs the emulator has on record: its mods
// folders and its per-game settings.
func knownTitleIDs(f *ftpConn, base string) []string {
	set := map[string]bool{}
	for id := range cheatFolders(f, path.Join(base, "mods")) {
		if titleIDFmt.MatchString(id) {
			set[id] = true
		}
	}
	// the settings file lives in the app's own data folder
	for _, p := range []string{path.Join(base, "config", "prosperoeden.json"), "/data/prosperoeden/config/prosperoeden.json"} {
		if b, err := f.get(p, 2<<20); err == nil {
			for _, m := range idAnywhere.FindAllStringSubmatch(string(b), -1) {
				set[baseTitle(m[1])] = true
			}
			break
		}
	}
	var out []string
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

type gameIDInfo struct {
	ID         string   `json:"id"`         // already in the file name
	Guess      string   `json:"guess"`      // Loadout's best answer, "" when it has none
	Why        string   `json:"why"`        // where the guess came from
	Candidates []string `json:"candidates"` // IDs on the PS5 that no game file claims yet
}

func gameIDLookup(f *ftpConn, gamesDir, name string) gameIDInfo {
	info := gameIDInfo{ID: nameID(name), Candidates: []string{}}
	if info.ID != "" {
		return info
	}
	if strings.EqualFold(path.Ext(name), ".nsp") {
		if id := nspTitleID(f, path.Join(gamesDir, name)); id != "" {
			info.Guess, info.Why = id, "read from the game file"
		}
	}
	claimed := map[string]bool{}
	unnamed := 0
	if ents, err := f.list(gamesDir); err == nil {
		for _, e := range ents {
			if e.IsDir || !isGameFile(e) {
				continue
			}
			if id := nameID(e.Name); id != "" {
				claimed[id] = true
			} else {
				unnamed++
			}
		}
	}
	for _, id := range knownTitleIDs(f, path.Dir(gamesDir)) {
		if !claimed[id] {
			info.Candidates = append(info.Candidates, id)
		}
	}
	if info.Guess == "" && unnamed == 1 && len(info.Candidates) == 1 {
		info.Guess, info.Why = info.Candidates[0], "the only game ID on the PS5 without a matching game file"
	}
	return info
}

// addIDToName renames a game file to carry its ID, and moves the cover and
// title the emulator saved for it so they aren't lost.
func (a *App) addIDToName(f *ftpConn, gamesDir, name, id string) (string, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	if !titleIDFmt.MatchString(id) {
		return "", errors.New("A game ID is 16 characters, starts with 01, and uses only 0-9 and A-F.")
	}
	ext := path.Ext(name)
	stem := strings.TrimSpace(idInName.ReplaceAllString(strings.TrimSuffix(name, ext), ""))
	if stem == "" {
		stem = "Game"
	}
	newName, err := fxName(stem + " [" + id + "]" + ext)
	if err != nil {
		return "", err
	}
	if newName == name {
		return name, nil
	}
	from, to := path.Join(gamesDir, name), path.Join(gamesDir, newName)
	if _, exists := f.size(to); exists {
		return "", errors.New("There's already a file with that name.")
	}
	if err := f.rename(from, to); err != nil {
		return "", fmt.Errorf("the PS5 didn't rename the file: %v", err)
	}
	// the emulator files its saved cover and title under a code made from the file name
	covers := path.Join(path.Dir(gamesDir), "covers")
	oldStem, newStem := fmt.Sprintf("%08x", fnv1a32(name)), fmt.Sprintf("%08x", fnv1a32(newName))
	for _, e := range []string{".tga", ".name"} {
		if _, ok := f.size(path.Join(covers, oldStem+e)); ok {
			f.rename(path.Join(covers, oldStem+e), path.Join(covers, newStem+e))
		}
	}
	oldKey, newKey := emuCoverPath(emuCoverKey(gamesDir, name)), emuCoverPath(emuCoverKey(gamesDir, newName))
	os.Rename(oldKey, newKey)
	os.Rename(oldKey+".name", newKey+".name")
	return newName, nil
}

func (a *App) gameIDRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/emu/gameid", func(r *http.Request) (any, error) {
		var b struct{ Dir, Name string }
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
		return gameIDLookup(f, dir, b.Name), nil
	})
	api("/api/emu/addid", func(r *http.Request) (any, error) {
		var b struct{ Dir, Name, ID string }
		body(r, &b)
		dir, ok := a.fxAllowed(b.Dir)
		if !ok || strings.ContainsAny(b.Name, "/\\") {
			return nil, errFxOutside
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		n, err := a.addIDToName(f, dir, b.Name, b.ID)
		return map[string]string{"name": n}, err
	})
	// every game whose ID Loadout can work out by itself
	api("/api/emu/addids", func(r *http.Request) (any, error) {
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
			return nil, err
		}
		done, already := 0, 0
		var left []string
		for _, e := range ents {
			if e.IsDir || !isGameFile(e) {
				continue
			}
			if nameID(e.Name) != "" {
				already++
				continue
			}
			info := gameIDLookup(f, dir, e.Name)
			if info.Guess != "" {
				if _, err := a.addIDToName(f, dir, e.Name, info.Guess); err == nil {
					done++
					continue
				}
			}
			left = append(left, e.Name)
		}
		return map[string]any{"done": done, "already": already, "left": left}, nil
	})
}
