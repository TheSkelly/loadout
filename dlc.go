package main

// dlc.go: finds downloadable content (DLC / add-ons) in and around game
// folders, and works out which game each piece belongs to.
//
// How DLC shows up in dumps:
//   - a folder with sce_sys/param.json (PS5) or param.sfo (PS4) but no
//     eboot.bin (only base games have an executable). PS4 add-ons are also
//     marked CATEGORY "ac" in param.sfo.
//   - a .pkg file whose header says it's additional content.
//   - anything inside a folder named DLC / Addcont inside a game's folder.
// Every DLC content ID embeds its game's title ID (UP9000-PPSA01234_00-...),
// which is how loose DLC is matched to its game.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type DLC struct {
	Path      string `json:"path"`
	File      string `json:"file"` // file or folder name
	Name      string `json:"name"`
	TitleID   string `json:"titleId"`
	ContentID string `json:"contentId"`
	Version   string `json:"version"`
	Kind      string `json:"kind"` // "Folder", "PKG", "Archive", "File"
	Size      int64  `json:"size"`
	Note      string `json:"note"`
	DumpRoot  bool   `json:"-"` // sce_sys sits directly inside (a raw DLC dump)
}

var contentIDRe = regexp.MustCompile(`([A-Z]{2}\d{4})-([A-Z]{4}\d{5})_(\d{2})-([A-Z0-9]{16})`)
var dlcFolderNames = map[string]bool{"dlc": true, "addcont": true, "add-ons": true, "addons": true, "dlcs": true}

func isDLCFolderName(n string) bool { return dlcFolderNames[strings.ToLower(strings.TrimSpace(n))] }

// ---------- PKG headers ----------

type pkgInfo struct {
	contentID string
	ctype     uint32 // 0x1A game, 0x1B add-on with data, 0x1C add-on license only, 0x1E patch
	title     string
	version   string
}

func (p pkgInfo) isDLC() bool { return p.ctype == 0x1B || p.ctype == 0x1C }

// readPkg reads a PS4-style PKG header (also used by PS4 games on PS5).
func readPkg(path string) (pkgInfo, bool) {
	var pi pkgInfo
	f, err := os.Open(path)
	if err != nil {
		return pi, false
	}
	defer f.Close()
	h := make([]byte, 0x100)
	if _, err := f.ReadAt(h, 0); err != nil || binary.BigEndian.Uint32(h) != 0x7F434E54 {
		return pi, false
	}
	pi.contentID = strings.TrimRight(string(h[0x40:0x64]), "\x00 ")
	pi.ctype = binary.BigEndian.Uint32(h[0x74:])
	// look for an unencrypted param.sfo in the entry table (fake PKGs)
	count := binary.BigEndian.Uint32(h[0x10:])
	table := int64(binary.BigEndian.Uint32(h[0x18:]))
	if count > 0 && count < 10000 {
		ents := make([]byte, int(count)*32)
		if _, err := f.ReadAt(ents, table); err == nil {
			for i := 0; i < int(count); i++ {
				e := ents[i*32:]
				if binary.BigEndian.Uint32(e) != 0x1000 { // param.sfo
					continue
				}
				off, size := int64(binary.BigEndian.Uint32(e[16:])), int(binary.BigEndian.Uint32(e[20:]))
				if size <= 0 || size > 1<<20 {
					break
				}
				sfo := make([]byte, size)
				if _, err := f.ReadAt(sfo, off); err == nil {
					tmp := filepath.Join(os.TempDir(), "titletag_param.sfo")
					if os.WriteFile(tmp, sfo, 0644) == nil {
						if m, err := readParamSFO(tmp); err == nil {
							pi.title, pi.version = m.Name, m.Version
						}
						os.Remove(tmp)
					}
				}
				break
			}
		}
	}
	return pi, true
}

// ---------- classification ----------

// dlcFromDir returns DLC info if dir is a DLC dump (param file, no eboot).
func dlcFromDir(dir string) (DLC, bool) {
	sys := filepath.Join(dir, "sce_sys")
	var m Meta
	var err error
	gameLike := fileExists(filepath.Join(dir, "eboot.bin")) || dirExists(filepath.Join(dir, "sce_module"))
	if m, err = readParamJSON(filepath.Join(sys, "param.json")); err != nil {
		if m, err = readParamSFO(filepath.Join(sys, "param.sfo")); err != nil {
			return DLC{}, false
		}
		if m.Category != "Add-on" && gameLike {
			return DLC{}, false
		}
	} else if gameLike || m.AppParams {
		return DLC{}, false
	}
	d := DLC{Path: dir, File: filepath.Base(dir), Name: m.Name, ContentID: m.ContentID, Version: m.Version, Kind: "Folder", TitleID: strings.ToUpper(m.TitleID)}
	fillDLCIDs(&d)
	return d, true
}

func dlcFromPkg(path string, insideGame bool) (DLC, bool) {
	pi, ok := readPkg(path)
	name := filepath.Base(path)
	if ok && !pi.isDLC() {
		return DLC{}, false
	}
	if !ok && !insideGame {
		// unreadable header: only call it DLC if the name says so
		if !regexp.MustCompile(`(?i)\b(dlc|addcont|add-on|addon)\b`).MatchString(name) && !contentIDRe.MatchString(strings.ToUpper(name)) {
			return DLC{}, false
		}
	}
	d := DLC{Path: path, File: name, Kind: "PKG", ContentID: pi.contentID, Name: pi.title, Version: pi.version}
	if pi.ctype == 0x1C {
		d.Kind = "PKG (unlock only)"
	}
	fillDLCIDs(&d)
	return d, true
}

func fillDLCIDs(d *DLC) {
	if d.ContentID == "" {
		if m := contentIDRe.FindString(strings.ToUpper(d.File)); m != "" {
			d.ContentID = m
		}
	}
	if d.TitleID == "" {
		if m := contentIDRe.FindStringSubmatch(d.ContentID); m != nil {
			d.TitleID = m[2]
		} else {
			d.TitleID = findID(d.File)
		}
	}
	if d.Name == "" {
		if m := contentIDRe.FindStringSubmatch(d.ContentID); m != nil {
			d.Name = m[4] // the add-on's label, e.g. SM2DLCSUIT000001
		} else {
			d.Name = strings.TrimSuffix(d.File, filepath.Ext(d.File))
		}
	}
}

// findDLCInside lists DLC inside a game's folder, skipping the game root.
func findDLCInside(folder, gameRoot string) []DLC {
	var out []DLC
	root := filepath.Clean(gameRoot)
	var walk func(dir string, depth int, inDLCDir bool)
	walk = func(dir string, depth int, inDLCDir bool) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if filepath.Clean(p) == root || strings.EqualFold(e.Name(), "sce_sys") {
				continue
			}
			if e.IsDir() {
				if d, ok := dlcFromDir(p); ok {
					d.DumpRoot = true
					out = append(out, d)
					continue
				}
				if isDLCFolderName(e.Name()) {
					walk(p, depth+1, true)
					continue
				}
				// only look a little deeper, and never inside the game itself
				if depth < 2 && !strings.HasPrefix(root, filepath.Clean(p)+string(os.PathSeparator)) {
					if inDLCDir {
						d, _ := identifyDLCFolder(p, e.Name())
						out = append(out, d)
						continue
					}
					walk(p, depth+1, false)
				} else if strings.HasPrefix(root, filepath.Clean(p)+string(os.PathSeparator)) {
					walk(p, depth+1, false)
				}
				continue
			}
			low := strings.ToLower(e.Name())
			if strings.HasSuffix(low, ".pkg") {
				if d, ok := dlcFromPkg(p, true); ok {
					out = append(out, d)
				}
				continue
			}
			if inDLCDir && !exIgnored[low] {
				kind := "File"
				if gameFileRe.MatchString(low) {
					kind = "Archive"
				}
				d := DLC{Path: p, File: e.Name(), Kind: kind}
				fillDLCIDs(&d)
				out = append(out, d)
			}
		}
	}
	walk(folder, 0, false)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func dlcSize(d *DLC) {
	if st, err := os.Stat(d.Path); err == nil {
		if st.IsDir() {
			d.Size = folderSize(d.Path)
		} else {
			d.Size = st.Size()
		}
	}
}

// isDLCPath reports whether path is (or is inside) one of the DLC items.
func isDLCPath(path string, dlc []DLC) bool {
	c := filepath.Clean(path)
	for _, d := range dlc {
		dp := filepath.Clean(d.Path)
		if c == dp || strings.HasPrefix(dp, c+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// ---------- identifying DLC folders ----------

var inclDLCRe = regexp.MustCompile(`(?i)(incl(uding|uded|\.)?|with|\+|and|w)[\s._-]*(all[\s._-]*)?(dlcs?|add-?ons?)`)

// isDLCName: the name says it's DLC, and not "game incl. DLC".
func isDLCName(name string) bool {
	return dlcWordRe.MatchString(name) && !inclDLCRe.MatchString(name)
}

var nfoIDRe = regexp.MustCompile(`(?i)title\s*id\s*:?\s*([A-Z]{4}\d{5})`)

// readNFO reads a scene .nfo (up to 2 folders deep) for its title ID and
// release title.
func readNFO(dir string) (id, title string) {
	var found string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if rel, _ := filepath.Rel(dir, p); strings.Count(rel, string(os.PathSeparator)) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".nfo") {
			found = p
		}
		return nil
	})
	if found == "" {
		return "", ""
	}
	b, err := os.ReadFile(found)
	if err != nil || len(b) > 1<<20 {
		return "", ""
	}
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || (r >= 32 && r < 127) {
			return r
		}
		return ' '
	}, string(b))
	if m := nfoIDRe.FindStringSubmatch(clean); m != nil {
		id = strings.ToUpper(m[1])
	}
	lines := strings.Split(clean, "\n")
	for i, l := range lines {
		if !strings.Contains(strings.ToUpper(strings.ReplaceAll(l, " ", "")), "PRESENTS") {
			continue
		}
		for _, n := range lines[i+1 : min(i+6, len(lines))] {
			n = strings.Trim(strings.Join(strings.Fields(strings.ReplaceAll(n, "*", " ")), " "), " :-=~")
			if len(n) > 3 {
				title = n
				break
			}
		}
		break
	}
	return id, title
}

var groupSuffixRe = regexp.MustCompile(`-[A-Za-z0-9]{2,12}$`)
var groupPrefixRe = regexp.MustCompile(`^[a-z0-9]{2,12}-`)

// sceneName turns RESIDENT.EVIL.requiem.Deluxe.Edition.DLC.Addon.PS5-DUPLEX
// into readable words.
func sceneName(s string) string {
	s = strings.TrimSuffix(s, filepath.Ext(s))
	if !strings.Contains(s, " ") {
		if loc := groupSuffixRe.FindStringIndex(s); loc != nil && strings.ContainsAny(s[:loc[0]], "._") {
			s = s[:loc[0]]
		}
		s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	}
	if groupPrefixRe.MatchString(s) && strings.ToLower(s) == s {
		s = groupPrefixRe.ReplaceAllString(s, "")
	}
	return strings.Join(strings.Fields(s), " ")
}

// identifyDLCFolder works out which game a folder of DLC belongs to and
// what it's called. found reports whether anything inside looked like DLC.
func identifyDLCFolder(p, name string) (DLC, bool) {
	d := DLC{Path: p, File: name, Kind: "Folder"}
	found := false
	var names []string
	inner := findDLCInside(p, "")
	for _, x := range inner {
		if x.Kind == "PKG" || x.Kind == "PKG (unlock only)" {
			if _, ok := dlcFromPkg(x.Path, false); !ok {
				continue
			}
		}
		found = true
		if d.TitleID == "" && x.TitleID != "" {
			d.TitleID = x.TitleID
		}
		if d.ContentID == "" && x.ContentID != "" {
			d.ContentID = x.ContentID
		}
		if d.Version == "" {
			d.Version = x.Version
		}
		if x.DumpRoot && x.Name != "" {
			names = append(names, x.Name)
		}
	}
	nfoID, nfoTitle := readNFO(p)
	if nfoTitle != "" {
		names = append(names, nfoTitle)
		if isDLCName(nfoTitle) {
			found = true
		}
	}
	if d.TitleID == "" {
		d.TitleID = nfoID
	}
	// PKGs we can't read (newer PS5 format) still carry a useful file name
	var pkgName string
	filepath.WalkDir(p, func(x string, e os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if rel, _ := filepath.Rel(p, x); rel != "." && strings.Count(rel, string(os.PathSeparator)) >= 2 {
				return filepath.SkipDir
			}
			if d.TitleID == "" && x != p {
				d.TitleID = findID(e.Name())
			}
			if x != p && fileExists(filepath.Join(x, "eboot.bin")) && !dirExists(filepath.Join(x, "sce_sys")) {
				d.Note = "Holds " + e.Name() + ", which is meant to be merged into the game's own folder. Check the release notes before using it."
			}
			return nil
		}
		if pkgName == "" && strings.EqualFold(filepath.Ext(x), ".pkg") {
			pkgName = e.Name()
		}
		return nil
	})
	if pkgName != "" {
		names = append(names, sceneName(pkgName))
	}
	names = append(names, sceneName(name))
	for _, n := range names {
		if strings.TrimSpace(n) != "" {
			d.Name = n
			break
		}
	}
	if d.TitleID == "" {
		d.TitleID = findID(name)
	}
	if d.ContentID == "" {
		fillDLCIDs(&d)
	}
	return d, found
}

// ---------- naming ----------

const defaultDLCPattern = "{GAME} [{ID}] DLC - {DLC}"

var dlcPresets = []string{
	"{GAME} [{ID}] DLC - {DLC}",
	"{DLC} [{ID}]",
	"{ID} DLC - {DLC}",
	"{GAME} - {DLC} [{LABEL}]",
}

var dlcTokenRe = regexp.MustCompile(`(?i)\{(GAME|ID|DLC|LABEL|VERSION)\}`)
var dlcFillerRe = regexp.MustCompile(`(?i)\b(dlcs?|add-?ons?|add on|addcont|ps[45]|incl(uding)?)\b`)

func dlcPatternProblem(p string) string {
	up := strings.ToUpper(p)
	if !strings.Contains(up, "{DLC}") && !strings.Contains(up, "{LABEL}") {
		return "The DLC pattern needs {DLC} in it."
	}
	return ""
}

func normWords(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		if accentMap[r] != "" {
			return rune(strings.ToLower(accentMap[r])[0])
		}
		return -1
	}, s)
}

// dlcLabel: UP9000-PPSA26344_00-YOTEILEGENDS0000 -> YOTEILEGENDS
func dlcLabel(contentID string) string {
	if m := contentIDRe.FindStringSubmatch(contentID); m != nil {
		return strings.TrimRight(m[4], "0")
	}
	return ""
}

// shortDLCName strips the game's name and filler words from a DLC name:
// "RESIDENT EVIL requiem Deluxe Edition DLC Addon" -> "Deluxe Edition".
func shortDLCName(raw, game string) string {
	s := strings.Join(strings.Fields(strings.NewReplacer("*", " ", "™", "", "®", "").Replace(raw)), " ")
	g := normWords(game)
	if g != "" {
		words := strings.Fields(s)
		acc := ""
		for i, w := range words {
			acc += normWords(w)
			if acc == g {
				s = strings.Join(words[i+1:], " ")
				break
			}
			if !strings.HasPrefix(g, acc) {
				break
			}
		}
	}
	s = dlcFillerRe.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " -:_.")
}

func buildDLCName(o NameOpts, game, id string, d DLC) string {
	pat := o.DLCPattern
	if dlcPatternProblem(pat) != "" {
		pat = defaultDLCPattern
	}
	dlc := shortDLCName(d.Name, game)
	label := dlcLabel(d.ContentID)
	if dlc == "" {
		dlc = label
	}
	if dlc == "" {
		dlc = "DLC"
	}
	ver := strings.TrimLeft(cleanName(d.Version), "vV")
	if ver == "" {
		pat = regexp.MustCompile(`(?i)\bv?\{VERSION\}`).ReplaceAllString(pat, "")
	}
	if label == "" {
		pat = regexp.MustCompile(`(?i)[\[(]?\{LABEL\}[\])]?`).ReplaceAllString(pat, "")
	}
	out := dlcTokenRe.ReplaceAllStringFunc(pat, func(t string) string {
		switch strings.ToUpper(t) {
		case "{GAME}":
			return cleanName(game)
		case "{ID}":
			return id
		case "{DLC}":
			return cleanName(dlc)
		case "{LABEL}":
			return label
		case "{VERSION}":
			return ver
		}
		return t
	})
	out = emptyBracketRe.ReplaceAllString(out, "")
	out = strings.Trim(cleanName(out), " -_")
	if o.Safe {
		out = safeName(out)
	}
	return out
}
