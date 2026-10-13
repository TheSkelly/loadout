package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func pngBytes(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// fakePkg lays out a PS4 package: header, entry table, then the entries.
func fakePkg(contentID string, ctype uint32, entries map[uint32][]byte, order []uint32) []byte {
	be := binary.BigEndian
	h := make([]byte, 0x1000)
	be.PutUint32(h, 0x7F434E54)
	be.PutUint32(h[0x10:], uint32(len(order)))
	be.PutUint32(h[0x18:], 0x1000)
	copy(h[0x40:], contentID)
	be.PutUint32(h[0x74:], ctype)
	table := make([]byte, len(order)*32)
	off := uint32(0x1000 + len(table))
	var body []byte
	for i, id := range order {
		e := table[i*32:]
		be.PutUint32(e, id)
		be.PutUint32(e[16:], off)
		be.PutUint32(e[20:], uint32(len(entries[id])))
		body = append(body, entries[id]...)
		off += uint32(len(entries[id]))
	}
	out := append(append(h, table...), body...)
	return append(out, make([]byte, 4096)...) // stand-in for the encrypted game data
}

func TestPkgGame(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ents := map[uint32][]byte{
		pkgEntrySFO:  sfoBytes("BioShock: The Collection", "CUSA03979"),
		pkgEntryIcon: pngBytes(64, 64, color.RGBA{30, 90, 160, 255}),
		pkgEntryPic1: pngBytes(320, 180, color.RGBA{120, 20, 40, 255}),
	}
	p := filepath.Join(dir, "some scene name-ps4.pkg") // no title ID in the file name
	os.WriteFile(p, fakePkg("UP1001-CUSA03979_00-BIOSHOCKCOLLECTN", 0x1A, ents, []uint32{pkgEntrySFO, pkgEntryPic1, pkgEntryIcon}), 0644)
	// an add-on package must not become a library game
	os.WriteFile(filepath.Join(dir, "dlc CUSA03979.pkg"), fakePkg("UP1001-CUSA03979_00-BIOSHOCKDLC00001", 0x1B, map[uint32][]byte{}, nil), 0644)
	// a file that only looks like a package is left alone
	os.WriteFile(filepath.Join(dir, "notes.pkg"), []byte("hello"), 0644)
	if out := os.Getenv("MKPKG"); out != "" {
		b, _ := os.ReadFile(p)
		os.WriteFile(out, b, 0644)
	}

	pg, ok := readPkgGame(p)
	if !ok || pg.TitleID != "CUSA03979" || pg.Title != "BioShock: The Collection" || !pg.isGame() {
		t.Fatalf("readPkgGame: %+v %v", pg, ok)
	}
	if img, _ := pkgImage(p, pkgEntryPic1, pkgEntryPic0); img == nil || img.Bounds().Dx() != 320 {
		t.Fatal("background art not read")
	}
	games, err := scan(dir, map[string]string{}, map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pkgs, dlc int
	for _, g := range games {
		t.Logf("%-28s pkg=%v dlc=%v id=%s name=%q", g.Current, g.IsPkg, g.IsDLC, g.TitleID, g.Name)
		if g.IsPkg {
			pkgs++
			if g.TitleID != "CUSA03979" || g.Meta.Platform != "PS4" {
				t.Errorf("bad pkg game: %+v", g.Meta)
			}
		}
		if g.IsDLC {
			dlc++
		}
	}
	if pkgs != 1 || dlc != 1 {
		t.Fatalf("want 1 pkg game and 1 dlc, got %d and %d", pkgs, dlc)
	}
}
