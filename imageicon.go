package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var titleIDInNameRe = regexp.MustCompile(`\[(PPSA\d{5}|CUSA\d{5})\]`)

// titleIDInName pulls the title ID out of a name like "NHL 27 [PPSA34063] v01.003.001.exfat".
func titleIDInName(name string) string {
	if m := titleIDInNameRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// maxIconBytes keeps a damaged image from making us read something huge.
const maxIconBytes = 8 << 20

// imageIcon reads sce_sys/icon0.png out of an exFAT image.
func imageIcon(image string) ([]byte, error) {
	im, root, err := openExImage(image)
	if err != nil {
		return nil, err
	}
	defer im.f.Close()
	find := func(ents []exEntry, name string, dir bool) (exEntry, bool) {
		for _, e := range ents {
			if e.isDir == dir && strings.EqualFold(e.name, name) {
				return e, true
			}
		}
		return exEntry{}, false
	}
	sys, ok := find(root, "sce_sys", true)
	if !ok {
		return nil, errors.New("no sce_sys folder in the image")
	}
	ents, err := im.listDir(sys.first, sys.length, sys.noChain)
	if err != nil {
		return nil, err
	}
	ic, ok := find(ents, "icon0.png", false)
	if !ok {
		return nil, errors.New("no icon0.png in the image")
	}
	if ic.length == 0 || ic.length > maxIconBytes {
		return nil, errors.New("icon0.png has an odd size")
	}
	return im.readAll(ic.first, ic.length, ic.noChain)
}

// cacheImageIcon saves an image's cover into the PS5 icon cache, once. Images
// that aren't on the console have no other source for a cover.
func cacheImageIcon(image string) {
	id := titleIDInName(filepath.Base(image))
	if id == "" {
		return
	}
	dst := ps5IconPath(id)
	if _, err := os.Stat(dst); err == nil {
		return
	}
	b, err := imageIcon(image)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return
	}
	os.WriteFile(dst, b, 0644)
}
