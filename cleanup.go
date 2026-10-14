package main

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Cleaning up the PS5's drives: what a stopped or failed copy leaves
// behind. Loadout lists it first; only what the list showed can be deleted.
//
//   - unfinished uploads: *.part (Loadout's FTP sends) and *.ava-part
//     (PS5Upload's, preallocated at full size, so they take a whole game's space)
//   - Loadout's own leftovers: .loadout-* files, a stale app update, packages
//     a DLC install didn't remove
//   - game folders with no sce_sys/param.json at the top, which
//     ShadowMountPlus can't mount: a folder send that stopped partway
//   - empty images (0 bytes)
//
// Partial files of downloads that are queued, running or paused are kept:
// those sends pick up from them.

type cleanItem struct {
	Path   string `json:"path"`
	Drive  string `json:"drive"`
	Reason string `json:"reason"`
	Size   int64  `json:"size"` // -1 for a folder (not added up)
	IsDir  bool   `json:"isDir"`
}

// cleanupScan finds what can go. kept counts partial files left alone
// because a download still needs them.
func (a *App) cleanupScan() (items []cleanItem, kept int, err error) {
	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	games := a.fxRootsLocked("games")
	brew := a.fxRootsLocked("homebrew")
	// the PS5 folders sends still in the queue are going to
	busy := map[string]bool{}
	for _, j := range a.build.Jobs {
		if j.Dest != "" && j.State != "done" && j.State != "failed" && j.State != "cancelled" && j.State != "skipped" {
			busy[path.Clean(j.Dest)] = true
		}
	}
	a.mu.Unlock()
	if host == "" {
		return nil, 0, errors.New("Set your PS5's IP address in Settings first.")
	}
	f, err := ftpDial(host, port)
	if err != nil {
		return nil, 0, errors.New("Loadout can't reach the PS5's FTP server.")
	}
	defer f.Close()

	add := func(p, drive, reason string, size int64, dir bool) {
		items = append(items, cleanItem{Path: p, Drive: drive, Reason: reason, Size: size, IsDir: dir})
	}
	partial := func(dir, name string) string {
		n := strings.ToLower(name)
		switch {
		case strings.HasSuffix(n, ".ava-part"):
			return "Unfinished PS5Upload copy"
		case strings.HasSuffix(n, ".part"):
			return "Unfinished upload"
		}
		return ""
	}
	scanFiles := func(dir, drive string, folders bool) {
		ents, err := f.list(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if e.Name == "." || e.Name == ".." {
				continue
			}
			p := path.Join(dir, e.Name)
			if strings.HasPrefix(e.Name, ".loadout-") {
				add(p, drive, "Loadout leftover", e.Size, e.IsDir)
				continue
			}
			if !e.IsDir {
				if why := partial(dir, e.Name); why != "" {
					if busy[path.Clean(dir)] || busy[p] {
						kept++
						continue
					}
					add(p, drive, why, e.Size, false)
					continue
				}
				low := strings.ToLower(e.Name)
				if e.Size == 0 && (strings.HasSuffix(low, ".exfat") || strings.HasSuffix(low, ".ffpkg") || strings.HasSuffix(low, ".ffpfsc")) {
					add(p, drive, "Empty image", 0, false)
				}
				continue
			}
			if !folders || strings.HasPrefix(e.Name, ".") {
				continue
			}
			// a game folder ShadowMountPlus could mount has sce_sys/param.json at the top
			if busy[p] {
				continue
			}
			if n, ok := f.size(path.Join(p, "sce_sys", "param.json")); ok && n > 0 {
				continue
			}
			if _, ok := f.size(path.Join(p, "eboot.bin")); ok {
				add(p, drive, "Game folder missing sce_sys/param.json", -1, true)
				continue
			}
			sub, _ := f.list(p)
			real := 0
			for _, s := range sub {
				if s.Name != "." && s.Name != ".." {
					real++
				}
			}
			if real == 0 {
				add(p, drive, "Empty folder", 0, true)
			} else if _, err := f.list(path.Join(p, "sce_sys")); err == nil {
				add(p, drive, "Game folder missing sce_sys/param.json", -1, true)
			}
		}
	}
	for _, r := range games {
		scanFiles(r.Path, r.Label, true)
	}
	for _, r := range brew {
		if strings.HasSuffix(r.Path, "/homebrew") {
			scanFiles(r.Path, r.Label, false) // apps and emulator folders aren't judged, only stray files
		}
	}
	// Loadout's own folder on the PS5
	if ents, err := f.list(pkgDropDir); err == nil {
		for _, e := range ents {
			if !e.IsDir && strings.HasSuffix(strings.ToLower(e.Name), ".pkg") && !busy[pkgDropDir] {
				add(path.Join(pkgDropDir, e.Name), "Internal", "Package left from a DLC install", e.Size, false)
			}
		}
	}
	if _, err := f.list(ps5AppOld); err == nil {
		add(ps5AppOld, "Internal", "Old copy of the Loadout PS5 app", -1, true)
	}
	return items, kept, nil
}

// cleanupDelete deletes the given paths, each of which a fresh scan must
// still list, so nothing else can be deleted through this.
func (a *App) cleanupDelete(paths []string) (string, error) {
	items, _, err := a.cleanupScan()
	if err != nil {
		return "", err
	}
	byPath := map[string]cleanItem{}
	for _, it := range items {
		byPath[it.Path] = it
	}
	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", errors.New("Loadout can't reach the PS5's FTP server.")
	}
	defer f.Close()
	var freed int64
	n, failed := 0, 0
	for _, p := range paths {
		it, ok := byPath[p]
		if !ok {
			continue
		}
		if err := f.removeAll(it.Path, it.IsDir, func() {}, func() bool { return false }); err != nil {
			failed++
			continue
		}
		n++
		if it.Size > 0 {
			freed += it.Size
		}
	}
	msg := fmt.Sprintf("Deleted %d item%s", n, plural(n))
	if freed > 0 {
		msg += ", " + humanSize(freed) + " freed"
	}
	if failed > 0 {
		msg += fmt.Sprintf(". %d couldn't be deleted", failed)
	}
	if n > 0 {
		go a.refreshPS5()
	}
	return msg + ".", nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
