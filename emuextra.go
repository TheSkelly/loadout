package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- ProsperoEden covers ----
// ProsperoEden keeps a 256x256 cover per game as covers/<hash>.tga, where the
// hash is FNV-1a (32-bit) of the game's file name, and the game's real title
// beside it as <hash>.name.

func fnv1a32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}

// decodeTGA reads the plain and run-length kinds of true-color TGA.
func decodeTGA(b []byte) (image.Image, error) {
	if len(b) < 18 {
		return nil, errors.New("not a TGA")
	}
	idLen, cmap, kind := int(b[0]), b[1], b[2]
	w, h := int(binary.LittleEndian.Uint16(b[12:])), int(binary.LittleEndian.Uint16(b[14:]))
	bpp, desc := int(b[16]), b[17]
	if cmap != 0 || (kind != 2 && kind != 10) || (bpp != 24 && bpp != 32) || w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, errors.New("unsupported TGA")
	}
	px := bpp / 8
	data := b[18+idLen:]
	raw := make([]byte, 0, w*h*px)
	if kind == 2 {
		if len(data) < w*h*px {
			return nil, errors.New("short TGA")
		}
		raw = data[:w*h*px]
	} else {
		for len(raw) < w*h*px {
			if len(data) < 1 {
				return nil, errors.New("short TGA")
			}
			n := int(data[0]&0x7f) + 1
			if data[0]&0x80 != 0 {
				if len(data) < 1+px {
					return nil, errors.New("short TGA")
				}
				for i := 0; i < n; i++ {
					raw = append(raw, data[1:1+px]...)
				}
				data = data[1+px:]
			} else {
				if len(data) < 1+n*px {
					return nil, errors.New("short TGA")
				}
				raw = append(raw, data[1:1+n*px]...)
				data = data[1+n*px:]
			}
		}
		raw = raw[:w*h*px]
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	topDown := desc&0x20 != 0
	for y := 0; y < h; y++ {
		sy := y
		if !topDown {
			sy = h - 1 - y
		}
		for x := 0; x < w; x++ {
			p := raw[(sy*w+x)*px:]
			a := uint8(255)
			if px == 4 {
				a = p[3]
			}
			img.SetNRGBA(x, y, color.NRGBA{p[2], p[1], p[0], a})
		}
	}
	return img, nil
}

// edenCovers fetches ProsperoEden's cover and title for each game file.
// Returns game file name -> cover key, and -> title.
func edenCovers(f *ftpConn, coversDir, gamesDir string, games []string) (covers, titles map[string]string) {
	covers, titles = map[string]string{}, map[string]string{}
	os.MkdirAll(filepath.Dir(emuCoverPath("x")), 0755)
	for _, g := range games {
		k := emuCoverKey(gamesDir, g)
		if b, err := os.ReadFile(emuCoverPath(k) + ".name"); err == nil {
			titles[g] = string(b)
		}
		if fileExists(emuCoverPath(k)) {
			covers[g] = k
			if titles[g] != "" {
				continue
			}
		}
		stem := fmt.Sprintf("%08x", fnv1a32(g))
		if titles[g] == "" {
			if b, err := f.get(path.Join(coversDir, stem+".name"), 2048); err == nil && len(b) > 0 {
				t := strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
				if t != "" {
					titles[g] = t
					os.WriteFile(emuCoverPath(k)+".name", []byte(t), 0644)
				}
			}
		}
		if covers[g] == "" {
			if b, err := f.get(path.Join(coversDir, stem+".tga"), 4<<20); err == nil && len(b) > 18 {
				if img, err := decodeTGA(b); err == nil {
					if out, err := os.Create(emuCoverPath(k)); err == nil {
						if png.Encode(out, img) == nil {
							covers[g] = k
						}
						out.Close()
						if covers[g] == "" {
							os.Remove(emuCoverPath(k))
						}
					}
				}
			}
		}
	}
	return
}

// ---- what was uploaded together ----
// Firmware is hundreds of files in one folder. Loadout remembers which files
// each upload brought, so the page can show one line per firmware.

type UploadGroup struct {
	Name  string
	When  time.Time
	Files []string
}

func (a *App) recordUpload(dir, name string, files []string) {
	if len(files) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.uploads == nil {
		a.uploads = map[string][]UploadGroup{}
	}
	gs := a.uploads[dir]
	kept := gs[:0]
	for _, g := range gs {
		if g.Name != name {
			kept = append(kept, g)
		}
	}
	a.uploads[dir] = append(kept, UploadGroup{Name: name, When: time.Now(), Files: files})
	saveJSON("emu-uploads.json", a.uploads)
}

type groupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type groupView struct {
	Name  string      `json:"name"`
	When  time.Time   `json:"when"`
	Known bool        `json:"known"` // false for the catch-all of files Loadout didn't upload
	Size  int64       `json:"size"`
	Files []groupFile `json:"files"`
}

// groupsFor sorts the files in dir into the uploads that brought them.
func (a *App) groupsFor(dir string, ents []ftpEntry) []groupView {
	have := map[string]int64{}
	for _, e := range ents {
		if !e.IsDir {
			have[e.Name] = e.Size
		}
	}
	a.mu.Lock()
	gs := append([]UploadGroup(nil), a.uploads[dir]...)
	a.mu.Unlock()
	sort.Slice(gs, func(i, j int) bool { return gs[i].When.After(gs[j].When) })
	used := map[string]bool{}
	out := []groupView{}
	for _, g := range gs {
		v := groupView{Name: g.Name, When: g.When, Known: true, Files: []groupFile{}}
		for _, fn := range g.Files {
			if sz, ok := have[fn]; ok && !used[fn] {
				used[fn] = true
				v.Files = append(v.Files, groupFile{fn, sz})
				v.Size += sz
			}
		}
		if len(v.Files) > 0 {
			out = append(out, v)
		}
	}
	rest := groupView{Name: "Added earlier", Files: []groupFile{}}
	for _, e := range ents {
		if !e.IsDir && !used[e.Name] && isGameFile(e) {
			rest.Files = append(rest.Files, groupFile{e.Name, e.Size})
			rest.Size += e.Size
		}
	}
	if len(rest.Files) > 0 {
		out = append(out, rest)
	}
	return out
}

// groupedDir reports whether dir is a folder shown one line per upload.
func groupedDir(dir string) bool {
	for _, k := range knownEmus {
		for _, x := range k.Extra {
			if x.Group && k.Base != "" && path.Join(k.Base, x.Dir) == dir {
				return true
			}
		}
	}
	return false
}
