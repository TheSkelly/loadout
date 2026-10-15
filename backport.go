package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
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

// gameLook is what a game's programs say, kept per image or game folder so
// each is read once (again when it changes).
type gameLook struct {
	Size    int64 // the image, or the folder's eboot.bin
	Mod     time.Time
	Fakelib bool   // a fakelib folder with files in it
	SDK     uint32 `json:",omitempty"` // the newest SDK any of its programs asks for
	V       int    `json:",omitempty"` // lookVersion when it was read
}

// lookVersion goes up when what's read changes, so older entries are read again
const lookVersion = 2

var (
	imageLooksMu sync.Mutex
	imageLooks   map[string]gameLook
	imageLooking bool
)

func imageLooksFile() string { return filepath.Join(appDir(), "imageinfo.json") }

// loadImageLooksLocked reads what was kept from earlier runs, the first time.
func loadImageLooksLocked() {
	if imageLooks == nil {
		imageLooks = map[string]gameLook{}
		if b, err := os.ReadFile(imageLooksFile()); err == nil {
			json.Unmarshal(b, &imageLooks)
		}
	}
}

// gameLookFor is what's known about an image or game folder so far; ones
// not read yet are read in the background. No disk here: this runs for every
// game on every refresh, under the app lock.
func gameLookFor(p string) (gameLook, bool) {
	imageLooksMu.Lock()
	defer imageLooksMu.Unlock()
	loadImageLooksLocked()
	l, ok := imageLooks[strings.ToLower(p)]
	if (!ok || l.V < lookVersion) && !imageLooking {
		imageLooking = true
		go lookAtImages()
	}
	return l, ok && l.V >= lookVersion
}

// lookStamp is what tells whether an image or folder changed since it was read.
func lookStamp(p string) (int64, time.Time, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, time.Time{}, false
	}
	if fi.IsDir() {
		if e, err := os.Stat(filepath.Join(p, "eboot.bin")); err == nil {
			return e.Size(), e.ModTime(), true
		}
		return 0, time.Time{}, false
	}
	return fi.Size(), fi.ModTime(), true
}

// lookAtImages reads the images and game folders not read yet.
func lookAtImages() {
	defer func() {
		imageLooksMu.Lock()
		imageLooking = false
		imageLooksMu.Unlock()
	}()
	var todo []string
	imageLooksMu.Lock()
	loadImageLooksLocked()
	var cands []string
	for _, dir := range imageLookDirs {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".exfat") {
				cands = append(cands, filepath.Join(dir, e.Name()))
			}
		}
	}
	cands = append(cands, folderLooks...)
	imageLooksMu.Unlock()
	for _, p := range cands {
		size, mod, ok := lookStamp(p)
		if !ok {
			continue
		}
		imageLooksMu.Lock()
		l, seen := imageLooks[strings.ToLower(p)]
		imageLooksMu.Unlock()
		if seen && l.V >= lookVersion && l.Size == size && l.Mod.Equal(mod) {
			continue
		}
		todo = append(todo, p)
	}
	for _, p := range todo {
		size, mod, ok := lookStamp(p)
		if !ok {
			continue
		}
		var c *backportCheck
		var err error
		if fi, e := os.Stat(p); e == nil && fi.IsDir() {
			c, err = checkFolderBackport(p)
		} else {
			c, err = checkImageBackport(p)
		}
		if err != nil {
			continue // a damaged or half-written image: try again later
		}
		imageLooksMu.Lock()
		imageLooks[strings.ToLower(p)] = gameLook{Size: size, Mod: mod, Fakelib: c.Fakelib > 0, SDK: c.highestValue, V: lookVersion}
		b, _ := json.Marshal(imageLooks)
		imageLooksMu.Unlock()
		os.WriteFile(imageLooksFile(), b, 0644)
	}
}

// imageLookDirs are the image folders to read and folderLooks the game
// folders, set from the library on each refresh.
var (
	imageLookDirs []string
	folderLooks   []string
)

var imageLookedAt time.Time

// setImageLookDirs sets what to read, and about once a minute has it all
// checked again (an image rebuilt under the same name is read again).
func setImageLookDirs(dirs, folders []string) {
	imageLooksMu.Lock()
	defer imageLooksMu.Unlock()
	imageLookDirs, folderLooks = dirs, folders
	if !imageLooking && time.Since(imageLookedAt) > time.Minute {
		imageLooking, imageLookedAt = true, time.Now()
		go lookAtImages()
	}
}

// backportState: what a game needs on this console. "needs" (built for newer
// firmware, not backported), "incomplete" (a fakelib folder, but its programs
// still ask for newer firmware: it won't start), "ok" (backported and checked),
// or "" (nothing to do, or not known yet).
func backportState(l gameLook, known bool, sdk, console string) (state, note string) {
	fw := sdkOfFirmware(console)
	if console == "" || fw == 0 {
		return "", ""
	}
	if known && l.SDK != 0 {
		have := sdkText(l.SDK)
		switch {
		case l.SDK > fw && l.Fakelib:
			return "incomplete", fmt.Sprintf("It has a fakelib folder, but its programs still ask for SDK %s, newer than your %s, so it won't start. The backport wasn't finished.", have, console)
		case l.SDK > fw:
			return "needs", fmt.Sprintf("Its programs ask for SDK %s, newer than your %s. It needs a backport to run.", have, console)
		case l.Fakelib && fwValue(sdk) > fwValue(console):
			return "ok", fmt.Sprintf("Backported: built with SDK %s, its programs now ask for %s and it has a fakelib folder.", sdk, have)
		}
		return "", ""
	}
	// not read yet: say nothing rather than guess from param.json, which
	// can't tell a backported game from one that isn't
	return "", ""
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
