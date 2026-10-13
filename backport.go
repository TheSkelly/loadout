package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Backports. What decides whether a game runs on the console's firmware is
// the SDK its programs were built with, not the firmware its param.json asks
// for (ShadowMountPlus and kstuff get past that check). A game built with a
// newer SDK runs once it's backported: its programs patched down and a
// "fakelib" folder of newer system libraries beside them, which
// ShadowMountPlus mounts when the game starts. Releases for newer firmware
// usually come backported already. Loadout looks for the fakelib folder in
// game folders and inside exFAT images, and flags the games that would
// still need it.

// imageLook is what an image's root says, kept per image so each is read once.
type imageLook struct {
	Size    int64
	Mod     time.Time
	Fakelib bool // a fakelib folder with files in it
}

var (
	imageLooksMu sync.Mutex
	imageLooks   map[string]imageLook
	imageLooking bool
)

func imageLooksFile() string { return filepath.Join(appDir(), "imageinfo.json") }

// loadImageLooksLocked reads what was kept from earlier runs, the first time.
func loadImageLooksLocked() {
	if imageLooks == nil {
		imageLooks = map[string]imageLook{}
		if b, err := os.ReadFile(imageLooksFile()); err == nil {
			json.Unmarshal(b, &imageLooks)
		}
	}
}

// imageBackported says whether an image holds a fakelib folder, from what's
// known so far; images not read yet are read in the background.
func imageBackported(p string) bool {
	imageLooksMu.Lock()
	defer imageLooksMu.Unlock()
	loadImageLooksLocked()
	// no disk here: this runs for every image on every refresh, under the app
	// lock. lookAtImages checks the images against what's kept, in the background.
	l, ok := imageLooks[strings.ToLower(p)]
	if !ok && !imageLooking {
		imageLooking = true
		go lookAtImages()
	}
	return ok && l.Fakelib
}

// lookAtImages reads the roots of the images not read yet.
func lookAtImages() {
	defer func() {
		imageLooksMu.Lock()
		imageLooking = false
		imageLooksMu.Unlock()
	}()
	var todo []string
	imageLooksMu.Lock()
	loadImageLooksLocked()
	for _, dir := range imageLookDirs {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() || !strings.EqualFold(filepath.Ext(p), ".exfat") {
				continue
			}
			fi, err := e.Info()
			if l, ok := imageLooks[strings.ToLower(p)]; err != nil || (ok && fi.Size() == l.Size && fi.ModTime().Equal(l.Mod)) {
				continue
			}
			todo = append(todo, p)
		}
	}
	imageLooksMu.Unlock()
	for _, p := range todo {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		fake, err := imageHasFakelib(p)
		if err != nil {
			continue // a damaged or half-written image: try again later
		}
		imageLooksMu.Lock()
		imageLooks[strings.ToLower(p)] = imageLook{Size: fi.Size(), Mod: fi.ModTime(), Fakelib: fake}
		b, _ := json.Marshal(imageLooks)
		imageLooksMu.Unlock()
		os.WriteFile(imageLooksFile(), b, 0644)
	}
}

// imageLookDirs are the image folders to read, set from the library's
// image folders on each refresh.
var imageLookDirs []string

var imageLookedAt time.Time

// setImageLookDirs sets the image folders, and about once a minute has the
// images checked again (an image rebuilt under the same name is read again).
func setImageLookDirs(dirs []string) {
	imageLooksMu.Lock()
	defer imageLooksMu.Unlock()
	imageLookDirs = dirs
	if !imageLooking && time.Since(imageLookedAt) > time.Minute {
		imageLooking, imageLookedAt = true, time.Now()
		go lookAtImages()
	}
}

// imageHasFakelib reads an exFAT image's root folder for a fakelib folder
// with files in it.
func imageHasFakelib(image string) (bool, error) {
	im, root, err := openExImage(image)
	if err != nil {
		return false, err
	}
	defer im.f.Close()
	for _, e := range root {
		if e.isDir && strings.EqualFold(e.name, "fakelib") {
			in, err := im.listDir(e.first, e.length, e.noChain)
			return err == nil && len(in) > 0, nil
		}
	}
	return false, nil
}

// openExImage opens an exFAT image and lists its root folder.
func openExImage(image string) (*exImage, []exEntry, error) {
	f, err := os.Open(image)
	if err != nil {
		return nil, nil, err
	}
	boot := make([]byte, 512)
	if _, err := f.ReadAt(boot, 0); err != nil || string(boot[3:11]) != "EXFAT   " {
		f.Close()
		return nil, nil, os.ErrInvalid
	}
	le := binary.LittleEndian
	bps := int64(1) << boot[108]
	im := &exImage{f: f, cs: bps << boot[109]}
	im.heap = int64(le.Uint32(boot[88:])) * bps
	im.clusters = le.Uint32(boot[92:])
	fatLen := int64(le.Uint32(boot[84:])) * bps
	if fatLen <= 0 || fatLen > 1<<30 {
		f.Close()
		return nil, nil, os.ErrInvalid
	}
	im.fat = make([]byte, fatLen)
	if _, err := f.ReadAt(im.fat, int64(le.Uint32(boot[80:]))*bps); err != nil {
		f.Close()
		return nil, nil, err
	}
	root := le.Uint32(boot[96:])
	n, c := uint32(0), root
	for c >= 2 && c-2 < im.clusters && n <= im.clusters {
		n++
		c = im.next(c)
	}
	ents, err := im.listDir(root, uint64(n)*uint64(im.cs), false)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return im, ents, nil
}

// needsBackport: a PS5 game built with an SDK newer than the console's
// firmware, with no fakelib folder to make up for it.
func needsBackport(e *LibEntry, console string, backported bool) bool {
	if e == nil || backported || e.Platform != "PS5" || console == "" || e.SDK == "" {
		return false
	}
	c, sdk := fwValue(console), fwValue(e.SDK)
	return c >= 0 && sdk > c
}
