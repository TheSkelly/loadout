package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"path/filepath"
	"strings"
)

// PS4 game packages (.pkg) carry their own details in plain sight: the name,
// title ID and version (param.sfo), the cover (icon0.png) and the background
// art (pic1.png / pic0.png). Loadout reads those so a PKG sits in the library
// like any other game. PS5 packages use a different format and aren't read.

const (
	pkgEntrySFO  = 0x1000
	pkgEntryPic1 = 0x1006
	pkgEntryIcon = 0x1200
	pkgEntryPic0 = 0x1220
)

// pkgGame is what a PS4 package says about itself.
type pkgGame struct {
	ContentID string
	TitleID   string
	Title     string
	Version   string
	Category  string // "gd" game, "gp" patch, "ac" add-on
	SysVer    string
}

// pkgEntry returns one file from a PS4 package's entry table (nil if absent).
func pkgEntry(path string, id uint32, max int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := make([]byte, 0x20)
	if _, err := f.ReadAt(h, 0); err != nil || binary.BigEndian.Uint32(h) != 0x7F434E54 {
		return nil
	}
	count := binary.BigEndian.Uint32(h[0x10:])
	table := int64(binary.BigEndian.Uint32(h[0x18:]))
	if count == 0 || count > 10000 {
		return nil
	}
	ents := make([]byte, int(count)*32)
	if _, err := f.ReadAt(ents, table); err != nil {
		return nil
	}
	for i := 0; i < int(count); i++ {
		e := ents[i*32:]
		if binary.BigEndian.Uint32(e) != id {
			continue
		}
		off, size := int64(binary.BigEndian.Uint32(e[16:])), int(binary.BigEndian.Uint32(e[20:]))
		if size <= 0 || size > max {
			return nil
		}
		b := make([]byte, size)
		if _, err := f.ReadAt(b, off); err != nil {
			return nil
		}
		return b
	}
	return nil
}

// readPkgGame reads a PS4 package's identity. ok is false for anything that
// isn't a readable PS4 package.
func readPkgGame(path string) (pkgGame, bool) {
	var g pkgGame
	pi, ok := readPkg(path)
	if !ok {
		return g, false
	}
	g.ContentID, g.Title, g.Version = pi.contentID, pi.title, pi.version
	g.TitleID = gameID(pi.contentID)
	if sfo := pkgEntry(path, pkgEntrySFO, 1<<20); sfo != nil {
		tmp := filepath.Join(os.TempDir(), "titletag_pkg.sfo")
		if os.WriteFile(tmp, sfo, 0644) == nil {
			if m, err := readParamSFO(tmp); err == nil {
				if m.Name != "" {
					g.Title = m.Name
				}
				if m.Version != "" {
					g.Version = m.Version
				}
				if m.TitleID != "" {
					g.TitleID = m.TitleID
				}
				g.Category, g.SysVer = strings.ToLower(m.Category), m.SysVer
			}
			os.Remove(tmp)
		}
	}
	if g.Category == "" {
		switch pi.ctype {
		case 0x1A:
			g.Category = "gd"
		case 0x1B, 0x1C:
			g.Category = "ac"
		}
	}
	return g, g.TitleID != ""
}

// isPkgGame says whether a package is a full PS4 game (not a patch or add-on).
func (g pkgGame) isGame() bool {
	return g.Category == "game" || g.Category == "app" || strings.HasPrefix(g.Category, "gd") || g.Category == ""
}

// pkgImage decodes one of a package's pictures.
func pkgImage(path string, ids ...uint32) (image.Image, []byte) {
	for _, id := range ids {
		if b := pkgEntry(path, id, 32<<20); b != nil {
			if img, _, err := image.Decode(bytes.NewReader(b)); err == nil {
				return img, b
			}
		}
	}
	return nil, nil
}
