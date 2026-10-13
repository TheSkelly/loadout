package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Home screen tiles for emulator games ("forwarders"). A forwarder is a tiny
// app folder in /data/homebrew: its own title ID, a picture, and a
// forwarder.json naming the game. Starting the tile asks ps5-app-launcher to
// open ProsperoEden with that game. The launcher program inside each
// forwarder (eboot.bin and its libc.prx) is Martin Pham's work from
// prospero-eden-forwarder.mph.am; Loadout doesn't ship it. The user points
// Loadout at one Forwarder.zip they made there, and Loadout reuses those two
// files for every game.

const fwdRoot = "/data/homebrew"

// emulators whose games can get a home screen tile, by title ID
var fwdTargets = map[string]bool{"PPSA99008": true}

func fwdTemplateDir() string { return filepath.Join(appDir(), "Forwarder", "template") }

func fwdHaveTemplate() bool {
	return fileExists(filepath.Join(fwdTemplateDir(), "eboot.bin")) && fileExists(filepath.Join(fwdTemplateDir(), "sce_module", "libc.prx"))
}

// fwdSetTemplate takes the launcher files out of a Forwarder.zip.
func fwdSetTemplate(zipPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return errors.New("That file couldn't be opened as a ZIP.")
	}
	defer zr.Close()
	want := map[string]string{"eboot.bin": "eboot.bin", "sce_module/libc.prx": filepath.Join("sce_module", "libc.prx")}
	got := map[string][]byte{}
	isFwd := false
	for _, zf := range zr.File {
		n := strings.ReplaceAll(zf.Name, "\\", "/")
		parts := strings.SplitN(n, "/", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == "forwarder.json" {
			isFwd = true
		}
		if _, ok := want[parts[1]]; ok && zf.UncompressedSize64 < 16<<20 {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(io.LimitReader(rc, 16<<20))
			rc.Close()
			got[parts[1]] = b
		}
	}
	if !isFwd || len(got["eboot.bin"]) < 1024 || len(got["sce_module/libc.prx"]) < 1024 {
		return errors.New("That ZIP isn't a forwarder made on prospero-eden-forwarder.mph.am (it needs eboot.bin, sce_module/libc.prx and forwarder.json inside one folder).")
	}
	dir := fwdTemplateDir()
	if err := os.MkdirAll(filepath.Join(dir, "sce_module"), 0755); err != nil {
		return err
	}
	for k, rel := range want {
		if err := os.WriteFile(filepath.Join(dir, rel), got[k], 0644); err != nil {
			return err
		}
	}
	return nil
}

// ---- pictures ----

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Rect, img, b.Min, draw.Src)
	return out
}

// resize scales a picture: averaging when shrinking, smooth blending when growing.
func resize(src *image.NRGBA, w, h int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if sw == 0 || sh == 0 {
		return out
	}
	if w <= sw && h <= sh {
		for y := 0; y < h; y++ {
			y0, y1 := y*sh/h, (y+1)*sh/h
			if y1 <= y0 {
				y1 = y0 + 1
			}
			for x := 0; x < w; x++ {
				x0, x1 := x*sw/w, (x+1)*sw/w
				if x1 <= x0 {
					x1 = x0 + 1
				}
				var r, g, b, a, n uint32
				for yy := y0; yy < y1; yy++ {
					p := src.Pix[yy*src.Stride+x0*4 : yy*src.Stride+x1*4]
					for i := 0; i < len(p); i += 4 {
						r += uint32(p[i])
						g += uint32(p[i+1])
						b += uint32(p[i+2])
						a += uint32(p[i+3])
						n++
					}
				}
				o := out.Pix[y*out.Stride+x*4:]
				o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
			}
		}
		return out
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*float64(sh)/float64(h) - 0.5
		if fy < 0 {
			fy = 0
		}
		y0 := int(fy)
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		wy := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(w) - 0.5
			if fx < 0 {
				fx = 0
			}
			x0 := int(fx)
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			wx := fx - float64(x0)
			a := src.Pix[y0*src.Stride+x0*4:]
			b := src.Pix[y0*src.Stride+x1*4:]
			c := src.Pix[y1*src.Stride+x0*4:]
			d := src.Pix[y1*src.Stride+x1*4:]
			o := out.Pix[y*out.Stride+x*4:]
			for k := 0; k < 4; k++ {
				top := float64(a[k])*(1-wx) + float64(b[k])*wx
				bot := float64(c[k])*(1-wx) + float64(d[k])*wx
				o[k] = uint8(top*(1-wy) + bot*wy + 0.5)
			}
		}
	}
	return out
}

// cropFit fills a w by h frame with the picture, trimming whatever overhangs.
func cropFit(src *image.NRGBA, w, h int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	cw, ch := sw, sw*h/w
	if ch > sh {
		cw, ch = sh*w/h, sh
	}
	if cw < 1 || ch < 1 {
		return image.NewNRGBA(image.Rect(0, 0, w, h))
	}
	x0, y0 := (sw-cw)/2, (sh-ch)/2
	crop := image.NewNRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(crop, crop.Rect, src, image.Pt(x0, y0), draw.Src)
	return resize(crop, w, h)
}

// boxBlur smooths a small picture in place; a few passes give a soft, even blur.
func boxBlur(img *image.NRGBA, radius, passes int) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	tmp := make([]uint8, len(img.Pix))
	for p := 0; p < passes; p++ {
		for pass := 0; pass < 2; pass++ { // across, then down
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					var sum [3]int
					n := 0
					for k := -radius; k <= radius; k++ {
						xx, yy := x, y
						if pass == 0 {
							xx += k
						} else {
							yy += k
						}
						if xx < 0 || yy < 0 || xx >= w || yy >= h {
							continue
						}
						q := img.Pix[yy*img.Stride+xx*4:]
						sum[0] += int(q[0])
						sum[1] += int(q[1])
						sum[2] += int(q[2])
						n++
					}
					o := tmp[y*img.Stride+x*4:]
					o[0], o[1], o[2], o[3] = uint8(sum[0]/n), uint8(sum[1]/n), uint8(sum[2]/n), 255
				}
			}
			copy(img.Pix, tmp)
		}
	}
}

// softBackdrop makes a blurred, darkened backdrop out of a tile picture.
func softBackdrop(icon *image.NRGBA, w, h int) *image.NRGBA {
	small := cropFit(icon, 160, 90)
	boxBlur(small, 6, 3)
	big := resize(small, w, h)
	for i := 0; i < len(big.Pix); i += 4 {
		big.Pix[i] = uint8(int(big.Pix[i]) * 60 / 100)
		big.Pix[i+1] = uint8(int(big.Pix[i+1]) * 60 / 100)
		big.Pix[i+2] = uint8(int(big.Pix[i+2]) * 60 / 100)
		big.Pix[i+3] = 255
	}
	return big
}

func plainTile() *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			o := out.Pix[y*out.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(24+y/16), uint8(33+y/12), uint8(49+(x+y)/14), 255
		}
	}
	return out
}

func loadPicture(p string) (*image.NRGBA, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, errors.New("that picture couldn't be read. Use a PNG or JPG")
	}
	if b := img.Bounds(); b.Dx() > 12000 || b.Dy() > 12000 || b.Dx() < 8 || b.Dy() < 8 {
		return nil, errors.New("that picture is too big or too small to use")
	}
	return toNRGBA(img), nil
}

// ---- BC7 (the texture format the PS5 wants for backgrounds) ----

var bc7w4 = [16]int{0, 4, 9, 13, 17, 21, 26, 30, 34, 38, 43, 47, 51, 55, 60, 64}

type bitWriter struct {
	b   *[16]byte
	pos uint
}

func (w *bitWriter) put(v uint32, n uint) {
	for i := uint(0); i < n; i++ {
		if v>>i&1 != 0 {
			w.b[w.pos>>3] |= 1 << (w.pos & 7)
		}
		w.pos++
	}
}

// bc7Block packs 16 opaque pixels (RGBA, row by row) into one 16-byte block,
// using the format's single-gradient layout (mode 6).
func bc7Block(px *[64]byte, out *[16]byte) {
	*out = [16]byte{}
	var lo, hi [3]int
	var mean [3]float64
	for c := 0; c < 3; c++ {
		lo[c], hi[c] = 255, 0
	}
	var lum [16]float64
	var lmean float64
	for i := 0; i < 16; i++ {
		for c := 0; c < 3; c++ {
			v := int(px[i*4+c])
			if v < lo[c] {
				lo[c] = v
			}
			if v > hi[c] {
				hi[c] = v
			}
			mean[c] += float64(v) / 16
		}
		lum[i] = 0.3*float64(px[i*4]) + 0.59*float64(px[i*4+1]) + 0.11*float64(px[i*4+2])
		lmean += lum[i] / 16
	}
	// a channel that falls as brightness rises runs the other way along the gradient
	var e0, e1 [3]int
	for c := 0; c < 3; c++ {
		var cov float64
		for i := 0; i < 16; i++ {
			cov += (float64(px[i*4+c]) - mean[c]) * (lum[i] - lmean)
		}
		e0[c], e1[c] = lo[c], hi[c]
		if cov < 0 {
			e0[c], e1[c] = hi[c], lo[c]
		}
	}
	// endpoints are stored as 7 bits plus a shared low bit of 1 (so alpha is a full 255)
	q := func(v int) int {
		t := (v - 1 + 1) / 2
		if t > 127 {
			t = 127
		}
		if t < 0 {
			t = 0
		}
		return t
	}
	var q0, q1 [3]int
	var d [3]float64
	var dd float64
	for c := 0; c < 3; c++ {
		q0[c], q1[c] = q(e0[c]), q(e1[c])
		a, b := q0[c]<<1|1, q1[c]<<1|1
		e0[c], e1[c] = a, b
		d[c] = float64(b - a)
		dd += d[c] * d[c]
	}
	var idx [16]int
	if dd > 0 {
		for i := 0; i < 16; i++ {
			var t float64
			for c := 0; c < 3; c++ {
				t += (float64(px[i*4+c]) - float64(e0[c])) * d[c]
			}
			k := int(t/dd*15 + 0.5)
			if k < 0 {
				k = 0
			}
			if k > 15 {
				k = 15
			}
			idx[i] = k
		}
	}
	if idx[0] >= 8 { // the first pixel's top bit isn't stored: flip the gradient
		q0, q1 = q1, q0
		for i := range idx {
			idx[i] = 15 - idx[i]
		}
	}
	w := bitWriter{b: out}
	w.put(1<<6, 7)
	for c := 0; c < 3; c++ {
		w.put(uint32(q0[c]), 7)
		w.put(uint32(q1[c]), 7)
	}
	w.put(127, 7)
	w.put(127, 7)
	w.put(1, 1)
	w.put(1, 1)
	w.put(uint32(idx[0]), 3)
	for i := 1; i < 16; i++ {
		w.put(uint32(idx[i]), 4)
	}
}

// ddsBC7 writes a picture as a BC7 .dds file, the way the PS5 reads backgrounds.
func ddsBC7(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx()&^3, img.Rect.Dy()&^3
	out := make([]byte, 148, 148+w*h)
	copy(out, "DDS ")
	le := binary.LittleEndian
	le.PutUint32(out[4:], 124)
	le.PutUint32(out[8:], 0x81007)
	le.PutUint32(out[12:], uint32(h))
	le.PutUint32(out[16:], uint32(w))
	le.PutUint32(out[20:], uint32(w*h))
	le.PutUint32(out[28:], 1)
	le.PutUint32(out[76:], 32)
	le.PutUint32(out[80:], 4)
	copy(out[84:], "DX10")
	le.PutUint32(out[108:], 0x1000)
	le.PutUint32(out[128:], 98) // BC7_UNORM
	le.PutUint32(out[132:], 3)
	le.PutUint32(out[140:], 1)
	var px [64]byte
	var blk [16]byte
	for by := 0; by < h; by += 4 {
		for bx := 0; bx < w; bx += 4 {
			for y := 0; y < 4; y++ {
				copy(px[y*16:y*16+16], img.Pix[(by+y)*img.Stride+bx*4:])
			}
			bc7Block(&px, &blk)
			out = append(out, blk[:]...)
		}
	}
	return out
}

// ---- building and sending ----

type fwdEntry struct {
	ID     string `json:"id"`
	Rom    string `json:"rom"`
	Target string `json:"target"`
}

// fwdList finds the forwarders already on the PS5.
func fwdList(f *ftpConn) []fwdEntry {
	out := []fwdEntry{}
	ents, err := f.list(fwdRoot)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.IsDir || gameID(e.Name) != e.Name {
			continue
		}
		b, err := f.get(path.Join(fwdRoot, e.Name, "forwarder.json"), 8<<10)
		if err != nil {
			continue
		}
		var c struct {
			Rom    string `json:"rom"`
			Target string `json:"target"`
		}
		if json.Unmarshal(b, &c) == nil && c.Rom != "" {
			out = append(out, fwdEntry{e.Name, c.Rom, c.Target})
		}
	}
	return out
}

type fwdSpec struct {
	Target string // the emulator's title ID
	Rom    string // file name inside the emulator's games folder
	Title  string
	Cover  string // key of the emulator's saved cover, "" when none
	Icon   string // a picture the user picked for the tile
	BG     string // a picture the user picked for the backgrounds
	Music  string // an .at9 file the user picked
	Exit   bool   // close the emulator when the game is quit
}

// buildForwarder writes a forwarder folder under dir and returns its path.
func buildForwarder(dir, id string, s fwdSpec) (string, error) {
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(filepath.Join(root, "sce_sys"), 0755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(root, "sce_module"), 0755); err != nil {
		return "", err
	}
	for _, rel := range []string{"eboot.bin", filepath.Join("sce_module", "libc.prx")} {
		b, err := os.ReadFile(filepath.Join(fwdTemplateDir(), rel))
		if err != nil {
			return "", errors.New("the forwarder launcher files are missing. Set them up again from a Forwarder.zip")
		}
		if err := os.WriteFile(filepath.Join(root, rel), b, 0644); err != nil {
			return "", err
		}
	}
	cfg, _ := json.MarshalIndent(map[string]any{"rom": s.Rom, "exit_after_game": s.Exit, "target": s.Target, "args": []string{}}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "forwarder.json"), cfg, 0644); err != nil {
		return "", err
	}
	param, _ := json.MarshalIndent(map[string]any{
		"ageLevel":                      map[string]int{"default": 0},
		"applicationCategoryType":       0,
		"applicationDrmType":            "free",
		"attribute":                     0,
		"attribute2":                    0,
		"attribute3":                    0,
		"conceptId":                     id[4:],
		"contentBadgeType":              1,
		"contentId":                     "UP9000-" + id + "_00-PROSPEROFORWARD0",
		"contentVersion":                "01.000.000",
		"downloadDataSize":              256,
		"gameIntent":                    map[string]any{"permittedIntents": []map[string]string{{"intentType": "launchActivity"}}},
		"localizedParameters":           map[string]any{"defaultLanguage": "en-US", "en-US": map[string]string{"titleName": s.Title}},
		"masterVersion":                 "01.00",
		"pubtools":                      map[string]string{"creationDate": time.Now().Format("2006-01-02 15:04:05"), "loudnessSnd0": "-28.00", "toolVersion": "2.00"},
		"requiredSystemSoftwareVersion": "0x0000000000000000",
		"sdkVersion":                    "0x0000000000000000",
		"titleId":                       id,
		"versionFileUri":                "",
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "sce_sys", "param.json"), param, 0644); err != nil {
		return "", err
	}
	// the tile picture: one the user picked, else the emulator's cover, else a plain tile
	var icon *image.NRGBA
	var err error
	switch {
	case s.Icon != "":
		if icon, err = loadPicture(s.Icon); err != nil {
			return "", err
		}
	case s.Cover != "" && fileExists(emuCoverPath(s.Cover)):
		icon, _ = loadPicture(emuCoverPath(s.Cover))
	}
	if icon == nil {
		icon = plainTile()
	}
	tile := cropFit(icon, 512, 512)
	var pb bytes.Buffer
	if err := png.Encode(&pb, tile); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "sce_sys", "icon0.png"), pb.Bytes(), 0644); err != nil {
		return "", err
	}
	// backgrounds: the picked picture, else a soft backdrop made from the tile
	var bg *image.NRGBA
	if s.BG != "" {
		src, err := loadPicture(s.BG)
		if err != nil {
			return "", err
		}
		bg = cropFit(src, 3840, 2160)
		for i := 3; i < len(bg.Pix); i += 4 {
			bg.Pix[i] = 255
		}
	} else {
		bg = softBackdrop(tile, 3840, 2160)
	}
	dds := ddsBC7(bg)
	for _, n := range []string{"pic0.dds", "pic1.dds"} {
		if err := os.WriteFile(filepath.Join(root, "sce_sys", n), dds, 0644); err != nil {
			return "", err
		}
	}
	if s.Music != "" {
		b, err := os.ReadFile(s.Music)
		if err != nil || len(b) < 64 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || len(b) > 64<<20 {
			return "", errors.New("that music file isn't an ATRAC9 (.at9) file the PS5 can play")
		}
		if err := os.WriteFile(filepath.Join(root, "sce_sys", "snd0.at9"), b, 0644); err != nil {
			return "", err
		}
	}
	return root, nil
}

// fwdNewID picks a title ID for a new tile: steady for a given game, and not
// one already used on the PS5.
func fwdNewID(rom string, taken map[string]bool) string {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(rom)))
	n := int(h.Sum32() % 9000)
	for i := 0; i < 9000; i++ {
		id := fmt.Sprintf("PPSA9%04d", (n+i)%9000+1000)
		if !taken[id] && !fwdTargets[id] && knownEmus[id].Name == "" {
			return id
		}
	}
	return ""
}

func (a *App) fwdRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/fwd/state", func(r *http.Request) (any, error) {
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return map[string]any{"template": fwdHaveTemplate(), "items": fwdList(f)}, nil
	})
	api("/api/fwd/template", func(r *http.Request) (any, error) {
		p, err := pickFile("Pick a Forwarder.zip made on prospero-eden-forwarder.mph.am", "ZIP files", "*.zip")
		if err != nil || p == "" {
			return map[string]any{"picked": false}, err
		}
		if err := fwdSetTemplate(p); err != nil {
			return nil, err
		}
		return map[string]any{"picked": true}, nil
	})
	api("/api/fwd/pick", func(r *http.Request) (any, error) {
		var b struct{ Kind string }
		body(r, &b)
		var p string
		var err error
		if b.Kind == "music" {
			p, err = pickFile("Pick background music (an ATRAC9 .at9 file)", "ATRAC9 audio", "*.at9")
		} else {
			p, err = pickFile("Pick a picture", "Pictures", "*.png;*.jpg;*.jpeg")
			if err == nil && p != "" {
				if _, err := loadPicture(p); err != nil {
					return nil, errors.New("That picture couldn't be read. Use a PNG or JPG.")
				}
			}
		}
		return map[string]any{"path": p, "name": filepath.Base(p)}, err
	})
	api("/api/fwd/make", func(r *http.Request) (any, error) {
		var s fwdSpec
		body(r, &s)
		s.Title, s.Rom = strings.TrimSpace(s.Title), strings.TrimSpace(s.Rom)
		if !fwdTargets[s.Target] {
			return nil, errors.New("Home screen tiles only work for ProsperoEden games.")
		}
		if s.Rom == "" || strings.ContainsAny(s.Rom, "\r\n\"") || s.Title == "" || len(s.Title) > 120 {
			return nil, errors.New("The tile needs a name and a game file.")
		}
		if !fwdHaveTemplate() {
			return nil, errors.New("Set up home screen tiles first: Loadout needs one Forwarder.zip to take the launcher files from.")
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		taken := map[string]bool{}
		if ents, err := f.list(fwdRoot); err == nil {
			for _, e := range ents {
				taken[e.Name] = true
			}
		}
		id := ""
		for _, it := range fwdList(f) {
			if it.Target == s.Target && it.Rom == s.Rom {
				id = it.ID // already has a tile: remake it in place
			}
		}
		f.Close()
		if id == "" {
			if id = fwdNewID(s.Rom, taken); id == "" {
				return nil, errors.New("Couldn't find a free ID for the tile.")
			}
		}
		// old working folders from earlier tiles
		if olds, _ := filepath.Glob(filepath.Join(os.TempDir(), "loadout-fwd-*")); len(olds) > 0 {
			for _, o := range olds {
				if fi, err := os.Stat(o); err == nil && time.Since(fi.ModTime()) > 6*time.Hour {
					os.RemoveAll(o)
				}
			}
		}
		tmp, err := os.MkdirTemp("", "loadout-fwd-")
		if err != nil {
			return nil, err
		}
		root, err := buildForwarder(tmp, id, s)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("Couldn't make the tile: %v", err)
		}
		dest := path.Join(fwdRoot, id)
		a.mu.Lock()
		j := &BuildJob{Key: "fx:" + dest, Kind: "fxupdir", Name: "Home screen tile for " + s.Title, Out: id, State: "queued", Size: folderSize(root), image: root, Dest: dest}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
		return map[string]any{"id": id}, nil
	})
	api("/api/fwd/remove", func(r *http.Request) (any, error) {
		var b struct{ ID string }
		body(r, &b)
		if gameID(b.ID) != b.ID || fwdTargets[b.ID] || knownEmus[b.ID].Name != "" {
			return nil, errors.New("That isn't a home screen tile Loadout can remove.")
		}
		f, err := a.fxDial()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		p := path.Join(fwdRoot, b.ID)
		if _, err := f.get(path.Join(p, "forwarder.json"), 8<<10); err != nil {
			return nil, errors.New("That folder isn't a forwarder, so it was left alone.")
		}
		if err := f.removeAll(p, true, func() {}, func() bool { return false }); err != nil {
			return nil, fmt.Errorf("Couldn't remove the tile's folder: %v", err)
		}
		return nil, nil
	})
}
