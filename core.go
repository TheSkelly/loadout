package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

var idRe = regexp.MustCompile(`([A-Z]{4})[-_]?(\d{5})`)

// Meta is everything we can learn about a game from its sce_sys folder.
type Meta struct {
	TitleID   string
	Name      string
	Source    string // "param.json" (PS5) or "param.sfo" (PS4)
	Platform  string // "PS5" / "PS4"
	ContentID string
	Version   string
	SysVer    string // required firmware
	SDK       string // SDK version the game was built with, e.g. "9.00"
	AppParams bool   // param.json has app-only settings (kernel, savedata...), so it's a game, not DLC
	Category  string
	SysDir    string // path to the sce_sys folder
	Root      string // the game root (folder holding sce_sys / eboot.bin)
}

type Game struct {
	Path     string
	Current  string
	TitleID  string
	Name     string
	Source   string
	NewName  string
	Status   string
	Checked  bool
	Done     bool
	IsFile   bool
	IsPkg    bool   // a PS4 game package (.pkg) read as a library game
	autoOff  bool   // unchecked by plan(), not by the user
	Wrap     bool   // game files sit loose in the folder; move them into <ID>-app
	Ext      string // for files: ".zip", ".part1.rar", ...
	Meta     Meta
	DLC      []DLC  // DLC already inside this game's folder
	Orphan   bool   // DLC whose game is only an exFAT image: renamed where it is
	IsDLC    bool   // this entry is loose DLC sitting next to the games
	DLCInfo  DLC    // details when IsDLC
	Parent   *Game  // game a loose DLC belongs to (nil if unknown)
	InPlace  bool   // DLC already in the game\'s DLC folder (renamed in place)
	Target   string // DLC: new name inside the game\'s DLC folder
	prevPath string
}

// Folders that should never be renamed or searched, whatever is inside them.
var defaultIgnored = []string{"Compressed", "Compressed Temp", "Downloading", "Downloads", "Temp", "Tmp", "Backup", "Backups", "Archive", "Archives", "Extracted", "Extracting", "Incomplete", "Updates", "DLC", "Patches", "$RECYCLE.BIN", "System Volume Information"}

var gameFileRe = regexp.MustCompile(`(?i)((\.part\d+)?\.(rar|zip|7z)|\.(pkg|ffpkg|ffpfs|exfat|img))$`)
var versionRe = regexp.MustCompile(`(?i)(\bv\d+(\.\d+)*\b|\b\d{2}\.\d{3}(\.\d{3})?\b)`)
var appSuffixRe = regexp.MustCompile(`(?i)[-_ ]?app\d?\b`)

// guessName pulls a readable name out of a folder/file name like
// "Ragnarok - PPSA08330-app0" or "PRAGMATA 01.200 PPSA02530".
func guessName(name, id string) string {
	s := name
	if id != "" {
		s = regexp.MustCompile(`(?i)[\[\(]?`+id[:4]+`[-_]?`+id[4:]+`[\]\)]?`).ReplaceAllString(s, " ")
	}
	s = appSuffixRe.ReplaceAllString(s, " ")
	s = versionRe.ReplaceAllString(s, " ")
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " -_.[]()")
}

func isIgnored(name string, ignored []string) bool {
	for _, x := range ignored {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// LibEntry is one game in the saved library.
type LibEntry struct {
	TitleID   string
	Name      string
	Platform  string
	ContentID string
	Version   string
	SysVer    string
	SDK       string
	Category  string
	Folder    string // full path, last place we saw it
	SizeBytes int64
	Icon      string // cached copy of icon0.png in the app data folder
	Thumb     string // small version for the list
	FirstSeen time.Time
	LastSeen  time.Time
	Present   bool
	Image     string     // exFAT image made from it, set when the folder was removed
	DLC       []DLC      // DLC found in its folder at the last scan
	Pkg       bool       `json:",omitempty"` // it's a PS4 package file, not a folder
	Hero      string     `json:",omitempty"` // big background art (from sce_sys/pic0.png)
	Store     *StoreInfo `json:",omitempty"` // PlayStation Store listing
	Backport  bool       `json:",omitempty"` // the game folder had a fakelib folder: it has been backported
}

var knownIDRe = regexp.MustCompile(`(PPSA|CUSA|PPLH|PLJM|PLJS|PCJS|PCAS|PLAS|ELJM|ECAS)[-_]?(\d{5})`)

func findID(s string) string {
	if m := knownIDRe.FindStringSubmatch(strings.ToUpper(s)); m != nil {
		return m[1] + m[2]
	}
	m := idRe.FindStringSubmatch(strings.ToUpper(s))
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

// ---------- metadata readers ----------

func readParamJSON(p string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(p)
	if err != nil {
		return m, err
	}
	b = []byte(strings.TrimPrefix(string(b), "\xef\xbb\xbf"))
	var pj struct {
		TitleID             string                     `json:"titleId"`
		ContentID           string                     `json:"contentId"`
		ContentVersion      string                     `json:"contentVersion"`
		MasterVersion       string                     `json:"masterVersion"`
		ReqSys              json.RawMessage            `json:"requiredSystemSoftwareVersion"`
		SDKVer              json.RawMessage            `json:"sdkVersion"`
		AppType             json.RawMessage            `json:"applicationCategoryType"`
		LocalizedParameters map[string]json.RawMessage `json:"localizedParameters"`
		Kernel              json.RawMessage            `json:"kernel"`
		Savedata            json.RawMessage            `json:"savedata"`
		GameIntent          json.RawMessage            `json:"gameIntent"`
	}
	if err = json.Unmarshal(b, &pj); err != nil {
		return m, err
	}
	m.AppParams = len(pj.Kernel) > 0 || len(pj.Savedata) > 0 || len(pj.GameIntent) > 0
	m.TitleID, m.ContentID, m.Source, m.Platform = pj.TitleID, pj.ContentID, "param.json", "PS5"
	m.Version = pj.ContentVersion
	if m.Version == "" {
		m.Version = pj.MasterVersion
	}
	m.Version = strings.TrimSpace(m.Version)
	if len(pj.ReqSys) > 0 {
		var s string
		if json.Unmarshal(pj.ReqSys, &s) == nil {
			m.SysVer = ps5FW(s)
		}
	}
	if len(pj.SDKVer) > 0 {
		var s string
		if json.Unmarshal(pj.SDKVer, &s) == nil {
			m.SDK = ps5FW(s)
		}
	}
	if len(pj.AppType) > 0 {
		var n int
		if json.Unmarshal(pj.AppType, &n) == nil {
			m.Category = map[int]string{0: "Game", 65536: "Native media app", 65537: "RNPS media app", 131328: "System built-in app", 131584: "Big daemon", 16777216: "ShellUI", 33554432: "Daemon", 67108864: "ShellApp"}[n]
		}
	}
	if m.Category == "" {
		m.Category = "Game"
	}
	lp := pj.LocalizedParameters
	title := func(lang string) string {
		raw, ok := lp[lang]
		if !ok {
			return ""
		}
		var v struct {
			TitleName string `json:"titleName"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return ""
		}
		return v.TitleName
	}
	var def string
	if raw, ok := lp["defaultLanguage"]; ok {
		json.Unmarshal(raw, &def)
	}
	for _, l := range []string{"en-US", "en-GB", def} {
		if l != "" {
			if t := title(l); t != "" {
				m.Name = t
				return m, nil
			}
		}
	}
	keys := make([]string, 0, len(lp))
	for k := range lp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t := title(k); t != "" {
			m.Name = t
			return m, nil
		}
	}
	return m, errors.New("no titleName")
}

// "0x0000000001000000" style -> "1.00"; anything else passes through.
func ps5FW(s string) string {
	s = strings.TrimSpace(s)
	h := strings.TrimPrefix(strings.ToLower(s), "0x")
	if len(h) == 16 {
		h = h[:8]
	}
	if len(h) == 8 {
		maj := strings.TrimLeft(h[:2], "0")
		if maj == "" {
			maj = "0"
		}
		return maj + "." + h[2:4]
	}
	return s
}

func ps4FW(v uint32) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%x.%02x", v>>24, (v>>16)&0xff)
}

func readParamSFO(p string) (Meta, error) {
	m := Meta{Source: "param.sfo", Platform: "PS4"}
	b, err := os.ReadFile(p)
	if err != nil {
		return m, err
	}
	if len(b) < 20 || string(b[:4]) != "\x00PSF" {
		return m, errors.New("not an SFO file")
	}
	le := binary.LittleEndian
	keyStart := int(le.Uint32(b[8:]))
	dataStart := int(le.Uint32(b[12:]))
	n := int(le.Uint32(b[16:]))
	cstr := func(x []byte) string {
		if i := strings.IndexByte(string(x), 0); i >= 0 {
			x = x[:i]
		}
		return strings.TrimSpace(string(x))
	}
	for i := 0; i < n; i++ {
		e := 20 + i*16
		if e+16 > len(b) {
			break
		}
		ko := keyStart + int(le.Uint16(b[e:]))
		fmtc := le.Uint16(b[e+2:])
		dl := int(le.Uint32(b[e+4:]))
		do := dataStart + int(le.Uint32(b[e+12:]))
		if ko >= len(b) || do+dl > len(b) || dl < 0 {
			continue
		}
		val := b[do : do+dl]
		switch cstr(b[ko:]) {
		case "TITLE":
			m.Name = cstr(val)
		case "TITLE_ID":
			m.TitleID = cstr(val)
		case "CONTENT_ID":
			m.ContentID = cstr(val)
		case "APP_VER":
			m.Version = cstr(val)
		case "VERSION":
			if m.Version == "" {
				m.Version = cstr(val)
			}
		case "SYSTEM_VER":
			if fmtc == 0x0404 && dl >= 4 {
				m.SysVer = ps4FW(le.Uint32(val))
			}
		case "CATEGORY":
			m.Category = map[string]string{"gd": "Game", "gp": "Game patch", "ac": "Add-on", "gde": "App", "bd": "Blu-ray"}[cstr(val)]
			if m.Category == "" {
				m.Category = cstr(val)
			}
		}
	}
	if m.Name == "" {
		return m, errors.New("no TITLE")
	}
	return m, nil
}

func tryDir(d string) (Meta, bool) {
	sys := filepath.Join(d, "sce_sys")
	m, ok := Meta{}, false
	hasEboot := fileExists(filepath.Join(d, "eboot.bin"))
	hasModules := dirExists(filepath.Join(d, "sce_module"))
	if pm, err := readParamJSON(filepath.Join(sys, "param.json")); err == nil && (hasEboot || hasModules || pm.AppParams) {
		m, ok = pm, true
	} else if pm, err := readParamSFO(filepath.Join(sys, "param.sfo")); err == nil && (hasEboot || hasModules) && pm.Category != "Add-on" {
		m, ok = pm, true
	} else if hasEboot && (dirExists(sys) || hasModules) {
		// a game root with no readable param file: still a game.
		// eboot.bin alone (no sce_sys) is a patch or DLC overlay, not a game.
		m, ok = Meta{Source: "eboot.bin"}, true
	}
	if !ok {
		return Meta{}, false
	}
	m.SysDir = sys
	m.Root = d
	if m.Name == "" {
		for _, p := range []string{filepath.Join(sys, "nptitle.dat"), filepath.Join(d, "nptitle.dat")} {
			if t := readFirstLine(p); t != "" {
				m.Name, m.Source = t, "nptitle.dat"
				break
			}
		}
	}
	if m.Version == "" {
		for _, p := range []string{filepath.Join(d, "pfs-version.dat"), filepath.Join(sys, "pfs-version.dat")} {
			if v := pfsVerRe.FindString(readFirstLine(p)); v != "" {
				m.Version = v
				break
			}
		}
	}
	return m, true
}

var pfsVerRe = regexp.MustCompile(`\d+(\.\d+)+`)

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func readFirstLine(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	t := strings.TrimPrefix(string(buf[:n]), "\xef\xbb\xbf")
	t = strings.Map(func(r rune) rune {
		if r == 0 {
			return '\n'
		}
		return r
	}, t)
	if i := strings.IndexAny(t, "\r\n"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// readMeta looks for sce_sys inside the folder, up to 3 levels deep.
func readMeta(dir string) (Meta, bool) {
	if m, ok := tryDir(dir); ok {
		return m, true
	}
	level := []string{dir}
	for depth := 0; depth < 3; depth++ {
		var next []string
		for _, d := range level {
			ents, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range ents {
				if !e.IsDir() || strings.EqualFold(e.Name(), "sce_sys") || isDLCFolderName(e.Name()) {
					continue
				}
				sub := filepath.Join(d, e.Name())
				if m, ok := tryDir(sub); ok {
					return m, true
				}
				next = append(next, sub)
			}
		}
		if len(next) > 400 {
			next = next[:400]
		}
		level = next
	}
	return Meta{}, false
}

// ---------- naming ----------

func cleanName(s string) string {
	s = strings.NewReplacer(
		"™", "", "®", "", "©", "", "℠", "",
		": ", " - ", ":", " - ",
		"/", "-", "\\", "-", "|", "-",
		"\"", "'", "?", "", "*", "", "<", "", ">", "",
		"\r", " ", "\n", " ", "\t", " ",
		"’", "'", "‘", "'", "“", "'", "”", "'",
	).Replace(s)
	s = strings.Map(func(r rune) rune {
		if r < 32 || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ". ")
	return strings.TrimSpace(s)
}

// NameOpts controls how new names are built.
type NameOpts struct {
	MoveDLC    bool   // move loose DLC into its game's folder and name DLC by DLCPattern
	DLCPattern string // e.g. "{GAME} [{ID}] DLC - {DLC}"
	WrapLoose  bool   // put loose game files into an <ID>-app subfolder
	Pattern    string // e.g. "{TITLE} [{ID}] v{VERSION}"
	Safe       bool   // PS5/loader-safe characters only
	// names of games that are only here as an exFAT image, by title ID, so
	// their DLC can still be named after them
	GameNames map[string]string
}

var patternPresets = []string{
	"{TITLE} [{ID}] v{VERSION}",
	"{TITLE} [{ID}]",
	"{TITLE} ({ID}) ({VERSION})",
	"{ID} - {TITLE} v{VERSION}",
	"{TITLE} [{ID}] [{PLATFORM}] v{VERSION}",
}

const defaultPattern = "{TITLE} [{ID}] v{VERSION}"

var tokenRe = regexp.MustCompile(`(?i)\{(TITLE|NAME|ID|PPSA|VERSION|VER|PLATFORM)\}`)
var emptyBracketRe = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}`)

// patternProblem returns a message if the pattern can't be used.
func patternProblem(p string) string {
	up := strings.ToUpper(p)
	if !strings.Contains(up, "{TITLE}") && !strings.Contains(up, "{NAME}") {
		return "The pattern needs {TITLE} in it."
	}
	if !strings.Contains(up, "{ID}") && !strings.Contains(up, "{PPSA}") {
		return "The pattern needs {ID} in it, so every folder keeps its game ID."
	}
	return ""
}

func buildName(o NameOpts, name, id, ver, platform string) string {
	name = cleanName(name)
	if name == "" {
		return ""
	}
	pat := o.Pattern
	if patternProblem(pat) != "" {
		pat = defaultPattern
	}
	ver = strings.TrimLeft(cleanName(ver), "vV")
	if id == "" {
		// no ID known: drop ID token and its brackets
		pat = tokenRe.ReplaceAllStringFunc(pat, func(t string) string {
			if u := strings.ToUpper(t); u == "{ID}" || u == "{PPSA}" {
				return ""
			}
			return t
		})
	}
	if ver == "" {
		// remove "v{VERSION}" as a unit so we don't leave a stray "v"
		pat = regexp.MustCompile(`(?i)\bv?\{(VERSION|VER)\}`).ReplaceAllString(pat, "")
	}
	out := tokenRe.ReplaceAllStringFunc(pat, func(t string) string {
		switch strings.ToUpper(t) {
		case "{TITLE}", "{NAME}":
			return name
		case "{ID}", "{PPSA}":
			return id
		case "{VERSION}", "{VER}":
			return ver
		case "{PLATFORM}":
			return platform
		}
		return t
	})
	out = emptyBracketRe.ReplaceAllString(out, "")
	out = cleanName(out)
	out = strings.Trim(out, " -_")
	if o.Safe {
		out = safeName(out)
	}
	return out
}

var accentMap = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'ā': "a", 'Á': "A", 'À': "A", 'Â': "A", 'Ä': "A", 'Ã': "A", 'Å': "A", 'Ā': "A",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'É': "E", 'È': "E", 'Ê': "E", 'Ë': "E", 'Ē': "E",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ī': "i", 'Í': "I", 'Ì': "I", 'Î': "I", 'Ï': "I", 'Ī': "I",
	'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ō': "o", 'ø': "o", 'Ó': "O", 'Ò': "O", 'Ô': "O", 'Ö': "O", 'Õ': "O", 'Ō': "O", 'Ø': "O",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ū': "u", 'Ú': "U", 'Ù': "U", 'Û': "U", 'Ü': "U", 'Ū': "U",
	'ñ': "n", 'Ñ': "N", 'ç': "c", 'Ç': "C", 'ß': "ss", 'æ': "ae", 'Æ': "AE", 'œ': "oe", 'Œ': "OE", 'ý': "y", 'ÿ': "y", 'Ý': "Y",
	'–': "-", '—': "-", '…': "...", '×': "x", '½': "1-2", '²': "2", '³': "3",
}

// safeName keeps only characters that are safe for PS5 loaders, exFAT
// labels and scripts: letters, digits, space and - _ . ( ) [ ] '
// Accented Latin letters are folded (ō -> o); other scripts are kept.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString(" and ")
		case r == '+':
			b.WriteString(" plus ")
		case accentMap[r] != "":
			b.WriteString(accentMap[r])
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" -_.()[]'", r):
			b.WriteRune(r)
		case r == '!' || r == '$' || r == '%' || r == '^' || r == '`' || r == '#' || r == ';' || r == '~' || r == '@' || r == ',' || r == '=' || r == '{' || r == '}':
			// dropped
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			// other non-ASCII (e.g. Japanese titles) is dropped
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = emptyBracketRe.ReplaceAllString(out, "")
	out = strings.Join(strings.Fields(out), " ")
	return strings.Trim(out, " -_.")
}

func plan(games []*Game, o NameOpts) {
	used := map[string]bool{}
	for _, g := range games {
		if g.Done {
			used[strings.ToLower(g.Current)] = true
		}
	}
	for _, g := range games {
		if g.Done || g.IsDLC {
			continue
		}
		base := buildName(o, g.Name, g.TitleID, g.Meta.Version, platformOf(g))
		g.NewName = ""
		if base == "" {
			g.Status = "No name found, click the name to type one"
			if g.Checked {
				g.autoOff = true
			}
			g.Checked = false
			continue
		}
		cand := base + g.Ext
		for i := 2; ; i++ {
			taken := used[strings.ToLower(cand)]
			if !taken && !strings.EqualFold(cand, g.Current) {
				if _, err := os.Stat(filepath.Join(filepath.Dir(g.Path), cand)); err == nil {
					taken = true
				}
			}
			if !taken {
				break
			}
			cand = fmt.Sprintf("%s (%d)", base, i) + g.Ext
		}
		used[strings.ToLower(cand)] = true
		g.NewName = cand
		if cand == g.Current && !(o.WrapLoose && g.Wrap) {
			g.Status = "Already named"
			if g.Checked {
				g.autoOff = true
			}
			g.Checked = false
		} else if g.Status == "" || strings.HasPrefix(g.Status, "Already named") || strings.HasPrefix(g.Status, "No name") || strings.HasPrefix(g.Status, "Ready") || strings.HasPrefix(g.Status, "Move files") || strings.HasPrefix(g.Status, "Rename and") {
			if g.autoOff {
				g.Checked, g.autoOff = true, false
			}
			g.Status = "Ready"
			if g.Source == "folder name" {
				g.Status = "Ready, name guessed from folder (check it)"
			}
			if o.WrapLoose && g.Wrap {
				if cand == g.Current {
					g.Status = "Move files into " + wrapName(g)
				} else {
					g.Status = "Rename and move files into " + wrapName(g)
				}
			}
		}
	}
	planDLC(games, o)
}
func (g *Game) displayName() string {
	if g.Name != "" {
		return g.Name
	}
	return g.Current
}

var orphanJunk = regexp.MustCompile(`(?i)\bdlc\b`)

// planDLC decides where each DLC goes and what it's called; call after
// the games are planned, since DLC lands in its game's (renamed) folder.
func planDLC(games []*Game, o NameOpts) {
	used := map[*Game]map[string]bool{}
	usedTop := map[string]bool{}
	for _, g := range games {
		if g.Done || !g.IsDLC {
			continue
		}
		g.NewName, g.Target, g.Orphan = "", "", false
		// its game is only an exFAT image: there's no folder to move into, but
		// the DLC can still take the DLC naming pattern right where it is
		if name := o.GameNames[g.TitleID]; g.Parent == nil && g.TitleID != "" && name != "" && o.MoveDLC {
			gameName := cleanName(name)
			if o.Safe {
				gameName = safeName(gameName)
			}
			// a name taken from the folder often just repeats "DLC" and the ID
			d := g.DLCInfo
			d.Name = strings.Trim(strings.Join(strings.Fields(orphanJunk.ReplaceAllString(knownIDRe.ReplaceAllString(strings.ReplaceAll(d.Name, "_", " "), " "), " ")), " "), " -[]()")
			base := strings.TrimSuffix(buildDLCName(o, gameName, g.TitleID, d), " - DLC")
			ext := ""
			if g.IsFile {
				if loc := gameFileRe.FindStringIndex(g.Current); loc != nil {
					ext = g.Current[loc[0]:]
				} else {
					ext = filepath.Ext(g.Current)
				}
			}
			cand := base
			for n := 2; usedTop[strings.ToLower(cand+ext)]; n++ {
				cand = fmt.Sprintf("%s (%d)", base, n)
			}
			usedTop[strings.ToLower(cand+ext)] = true
			g.Target, g.NewName, g.Orphan = cand+ext, cand+ext, true
			if strings.EqualFold(g.Target, g.Current) {
				g.Status = "Already named"
				if g.Checked {
					g.autoOff = true
				}
				g.Checked = false
				continue
			}
			g.Status = "Rename to the DLC naming pattern. Its game (" + name + ") is an exFAT image, so it stays in this folder"
			if g.autoOff {
				g.Checked, g.autoOff = true, false
			}
			continue
		}
		on := true
		switch {
		case g.Parent == nil && g.TitleID == "":
			g.Status, on = "DLC, couldn't tell which game it's for. Left alone", false
		case g.Parent == nil:
			g.Status, on = "DLC for "+g.TitleID+", that game isn't in this folder. Left alone", false
		case !o.MoveDLC:
			g.Status, on = "DLC for "+g.Parent.displayName()+". Organizing DLC is turned off", false
		}
		if !on {
			if g.Checked {
				g.autoOff = true
			}
			g.Checked = false
			continue
		}
		par := g.Parent
		gameName := cleanName(par.displayName())
		if o.Safe {
			gameName = safeName(gameName)
		}
		base := buildDLCName(o, gameName, par.TitleID, g.DLCInfo)
		ext := ""
		if g.IsFile {
			if loc := gameFileRe.FindStringIndex(g.Current); loc != nil {
				ext = g.Current[loc[0]:]
			} else {
				ext = filepath.Ext(g.Current)
			}
		}
		if used[par] == nil {
			used[par] = map[string]bool{}
		}
		cand := base
		for n := 2; used[par][strings.ToLower(cand+ext)]; n++ {
			cand = fmt.Sprintf("%s (%d)", base, n)
		}
		used[par][strings.ToLower(cand+ext)] = true
		g.Target = cand + ext
		g.NewName = filepath.Join("DLC", g.Target)
		if g.DLCInfo.DumpRoot {
			g.NewName = filepath.Join("DLC", g.Target, g.Current)
		}
		if g.InPlace && !g.DLCInfo.DumpRoot && strings.EqualFold(g.Target, g.Current) {
			g.Status = "Already named"
			if g.Checked {
				g.autoOff = true
			}
			g.Checked = false
			continue
		}
		if g.InPlace {
			g.Status = "Rename to the DLC naming pattern"
		} else {
			g.Status = "Move into " + par.displayName() + "'s DLC folder"
		}
		if g.autoOff {
			g.Checked, g.autoOff = true, false
		}
	}
}

// looseDLC decides whether a top-level folder with no game in it is DLC.
func looseDLC(p, name string) (DLC, bool) {
	if d, ok := dlcFromDir(p); ok {
		d.DumpRoot = true
		return d, true
	}
	d, found := identifyDLCFolder(p, name)
	if found || isDLCName(name) {
		return d, true
	}
	return DLC{}, false
}

var dlcWordRe = regexp.MustCompile(`(?i)(^|[^a-z])(dlc|addcont|add-?ons?)([^a-z]|$)`)

// scan lists the game folders and game files directly inside root.
// known holds names learned earlier (library) keyed by title ID.
func scan(root string, saved, known map[string]string, ignored []string) ([]*Game, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var games []*Game
	metaNames := map[string]string{}
	metaVers := map[string]string{}
	for _, e := range ents {
		nm := e.Name()
		if isIgnored(nm, ignored) || strings.HasPrefix(nm, ".") {
			continue
		}
		p := filepath.Join(root, nm)
		if !e.IsDir() {
			loc := gameFileRe.FindStringIndex(nm)
			if loc == nil {
				continue
			}
			stem, ext := nm[:loc[0]], nm[loc[0]:]
			if strings.HasSuffix(strings.ToLower(nm), ".pkg") {
				if d, isDLC := dlcFromPkg(p, false); isDLC {
					games = append(games, &Game{Path: p, Current: nm, TitleID: d.TitleID, Name: d.Name, IsDLC: true, IsFile: true, DLCInfo: d})
					continue
				}
				// a PS4 game package describes itself, so it's a library game
				if pg, ok := readPkgGame(p); ok && pg.isGame() && !isIgnored(stem, ignored) {
					g := &Game{Path: p, Current: nm, TitleID: pg.TitleID, Name: pg.Title, IsFile: true, IsPkg: true, Ext: ext, Source: "param.sfo"}
					g.Meta = Meta{TitleID: pg.TitleID, Name: pg.Title, Source: "param.sfo", Platform: "PS4", ContentID: pg.ContentID, Version: pg.Version, SysVer: pg.SysVer, Category: "Game"}
					if strings.HasPrefix(pg.TitleID, "PPSA") {
						g.Meta.Platform = "PS5"
					}
					if s := saved[pg.TitleID]; s != "" {
						g.Name, g.Source = s, "saved"
					}
					games = append(games, g)
					continue
				}
			}
			if dlcWordRe.MatchString(stem) {
				d := DLC{Path: p, File: nm, Kind: "Archive"}
				fillDLCIDs(&d)
				games = append(games, &Game{Path: p, Current: nm, TitleID: d.TitleID, Name: d.Name, IsDLC: true, IsFile: true, DLCInfo: d})
				continue
			}
			id := findID(stem)
			if id == "" || isIgnored(stem, ignored) {
				continue // can't tell what game it is, leave it alone
			}
			games = append(games, &Game{Path: p, Current: nm, TitleID: id, IsFile: true, Ext: ext})
			continue
		}
		m, ok := readMeta(p)
		if !ok {
			if d, isDLC := looseDLC(p, nm); isDLC {
				games = append(games, &Game{Path: p, Current: nm, TitleID: d.TitleID, Name: d.Name, IsDLC: true, DLCInfo: d})
				continue
			}
		}
		id := strings.ToUpper(strings.TrimSpace(m.TitleID))
		if id == "" {
			id = findID(nm)
		}
		if id == "" && !ok {
			continue // not a game folder
		}
		m.TitleID = id
		g := &Game{Path: p, Current: nm, TitleID: id, Name: m.Name, Source: m.Source, Meta: m}
		// loose layout: the game root IS this folder (sce_sys/eboot.bin directly inside)
		g.Wrap = ok && id != "" && filepath.Clean(m.Root) == filepath.Clean(p)
		if ok {
			g.DLC = findDLCInside(p, m.Root)
			for i := range g.DLC {
				dlcSize(&g.DLC[i])
			}
		}
		if ok && id != "" && m.Name != "" {
			metaNames[id] = m.Name
		}
		if ok && id != "" && m.Version != "" {
			metaVers[id] = m.Version
		}
		games = append(games, g)
	}
	// fill in names: manual > metadata > same game elsewhere in folder > library > guess
	byID := map[string]*Game{}
	for _, g := range games {
		if !g.IsDLC && !g.IsFile && g.TitleID != "" && g.Meta.Root != "" {
			if _, dup := byID[g.TitleID]; !dup {
				byID[g.TitleID] = g
			}
		}
	}
	// DLC already in a game's DLC folder can be renamed in place
	var inPlace []*Game
	for _, g := range games {
		if g.IsDLC || g.IsFile {
			continue
		}
		for _, d := range g.DLC {
			parent := filepath.Dir(d.Path)
			if !strings.EqualFold(filepath.Base(parent), "DLC") || filepath.Clean(filepath.Dir(parent)) != filepath.Clean(g.Path) {
				continue // DLC elsewhere in the game's folder is left alone
			}
			st, err := os.Stat(d.Path)
			if err != nil {
				continue
			}
			inPlace = append(inPlace, &Game{Path: d.Path, Current: filepath.Base(d.Path), TitleID: g.TitleID, Name: d.Name,
				IsDLC: true, InPlace: true, IsFile: !st.IsDir(), DLCInfo: d, Parent: g, Checked: true})
		}
	}
	for _, g := range games {
		if g.IsDLC {
			g.Parent = byID[g.TitleID]
			g.Checked = g.Parent != nil
			dlcSize(&g.DLCInfo)
			continue
		}
		if s, has := saved[g.TitleID]; has && g.TitleID != "" {
			g.Name, g.Source = s, "saved"
		} else if g.Name != "" {
		} else if n := metaNames[g.TitleID]; n != "" && g.TitleID != "" {
			g.Name, g.Source = n, "matched"
		} else if n := known[g.TitleID]; n != "" && g.TitleID != "" {
			g.Name, g.Source = n, "library"
		} else {
			stem := strings.TrimSuffix(g.Current, g.Ext)
			if n := guessName(stem, g.TitleID); n != "" {
				g.Name, g.Source = n, "folder name"
			}
		}
		g.Checked = g.Name != ""
		// version: metadata > same game elsewhere in folder > old name
		if g.Meta.Version == "" && g.TitleID != "" {
			g.Meta.Version = metaVers[g.TitleID]
		}
		if g.Meta.Version == "" {
			if v := versionRe.FindString(strings.TrimSuffix(g.Current, g.Ext)); v != "" {
				g.Meta.Version = strings.TrimLeft(v, "vV")
			}
		}
	}
	sort.Slice(games, func(i, j int) bool { return strings.ToLower(games[i].Current) < strings.ToLower(games[j].Current) })
	return append(games, inPlace...), nil
}

// ---------- library ----------

type Library struct {
	Root  string
	Games map[string]*LibEntry // key: title ID, or "path:<folder>" when unknown
}

func libKey(g *Game) string {
	if g.TitleID != "" {
		return g.TitleID
	}
	return "path:" + strings.ToLower(g.Path)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// updateLibrary merges a fresh scan into the library. Games from the same
// root folder that weren't seen this time get marked missing.
func updateLibrary(lib *Library, root string, games []*Game) {
	if lib.Games == nil {
		lib.Games = map[string]*LibEntry{}
	}
	lib.Root = root
	now := time.Now()
	seen := map[string]bool{}
	iconDir := filepath.Join(appDir(), "icons")
	os.MkdirAll(iconDir, 0755)
	for _, g := range games {
		if (g.IsFile && !g.IsPkg) || g.IsDLC {
			continue
		}
		k := libKey(g)
		seen[k] = true
		e := lib.Games[k]
		if e == nil {
			e = &LibEntry{FirstSeen: now}
			lib.Games[k] = e
		}
		m := g.Meta
		e.TitleID, e.Folder, e.LastSeen, e.Present = g.TitleID, g.Path, now, true
		if g.Name != "" {
			e.Name = g.Name
		}
		set := func(dst *string, v string) {
			if v != "" {
				*dst = v
			}
		}
		set(&e.Platform, m.Platform)
		set(&e.ContentID, m.ContentID)
		set(&e.Version, m.Version)
		set(&e.SysVer, m.SysVer)
		set(&e.SDK, m.SDK)
		e.DLC = g.DLC
		if m.Root != "" {
			e.Backport = isBackported(m.Root, g.Path)
		}
		set(&e.Category, m.Category)
		if e.Platform == "" {
			if strings.HasPrefix(g.TitleID, "PPSA") || strings.HasPrefix(g.TitleID, "PPLH") {
				e.Platform = "PS5"
			} else if strings.HasPrefix(g.TitleID, "CUSA") {
				e.Platform = "PS4"
			}
		}
		e.Pkg = g.IsPkg
		if g.IsPkg {
			if fi, err := os.Stat(g.Path); err == nil {
				e.SizeBytes = fi.Size()
			}
			safe := strings.NewReplacer(":", "_", "\\", "_", "/", "_").Replace(k)
			dst := filepath.Join(iconDir, safe+".png")
			if _, raw := pkgImage(g.Path, pkgEntryIcon); raw != nil && os.WriteFile(dst, raw, 0644) == nil {
				e.Icon = dst
				th := filepath.Join(iconDir, safe+"_s.png")
				if makeThumb(dst, th, 48) == nil {
					e.Thumb = th
				}
			}
		}
		if m.SysDir != "" {
			src := filepath.Join(m.SysDir, "icon0.png")
			if _, err := os.Stat(src); err == nil {
				safe := strings.NewReplacer(":", "_", "\\", "_", "/", "_").Replace(k)
				dst := filepath.Join(iconDir, safe+".png")
				if copyFile(src, dst) == nil {
					e.Icon = dst
					th := filepath.Join(iconDir, safe+"_s.png")
					if makeThumb(dst, th, 48) == nil {
						e.Thumb = th
					}
				}
			}
		}
	}
	for k, e := range lib.Games {
		if !seen[k] && sameRoot(e.Folder, root) {
			e.Present = false
		}
	}
}

func sameRoot(folder, root string) bool {
	return strings.EqualFold(filepath.Clean(filepath.Dir(folder)), filepath.Clean(root))
}

// folderSize adds up every file under p. Several folders are read at
// once, which matters on big games (God of War Ragnarok has ~376k files).
func folderSize(p string) int64 {
	var (
		mu      sync.Mutex
		cond    = sync.NewCond(&mu)
		queue   = []string{p}
		pending = 1
		total   int64
		wg      sync.WaitGroup
	)
	worker := func() {
		defer wg.Done()
		for {
			mu.Lock()
			for len(queue) == 0 && pending > 0 {
				cond.Wait()
			}
			if pending == 0 {
				mu.Unlock()
				return
			}
			dir := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			mu.Unlock()

			var sum int64
			var subs []string
			if ents, err := os.ReadDir(dir); err == nil {
				for _, e := range ents {
					if e.IsDir() {
						subs = append(subs, filepath.Join(dir, e.Name()))
					} else if e.Type().IsRegular() {
						if fi, err := e.Info(); err == nil {
							sum += fi.Size()
						}
					}
				}
			}
			atomic.AddInt64(&total, sum)
			mu.Lock()
			queue = append(queue, subs...)
			pending += len(subs) - 1
			mu.Unlock()
			cond.Broadcast()
		}
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go worker()
	}
	wg.Wait()
	return total
}

func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

func (lib *Library) Sorted() []*LibEntry {
	var out []*LibEntry
	for _, e := range lib.Games {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Present != out[j].Present {
			return out[i].Present
		}
		return strings.ToLower(sortName(out[i])) < strings.ToLower(sortName(out[j]))
	})
	return out
}

func csvEsc(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func exportCSV(lib *Library, path string) error {
	var sb strings.Builder
	sb.WriteString("\xef\xbb\xbfName,Title ID,Platform,Version,Required FW,Content ID,Size,Status,Folder,First Seen,Last Seen\n")
	for _, e := range lib.Sorted() {
		st := "In folder"
		if !e.Present {
			st = "Missing"
		}
		row := []string{sortName(e), e.TitleID, e.Platform, e.Version, e.SysVer, e.ContentID, humanSize(e.SizeBytes), st, e.Folder,
			e.FirstSeen.Format("2006-01-02"), e.LastSeen.Format("2006-01-02")}
		for i, c := range row {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(csvEsc(c))
		}
		sb.WriteString("\r\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

// ---------- undo + storage ----------

// move is one undoable step. Wrap moves mean "the contents of From were
// moved into the new subfolder To".
type move struct {
	From, To string
	Wrap     bool
	Mkdir    bool // To is a folder that was created; undo removes it if empty
}

func wrapName(g *Game) string { return g.TitleID + "-app" }

// wrapFolder moves everything inside dir into dir/<sub>. On any failure it
// moves back whatever it already moved and removes the new folder.
func wrapFolder(dir, sub string, keep func(path string) bool) (string, error) {
	inner := filepath.Join(dir, sub)
	if _, err := os.Stat(inner); err == nil {
		return "", fmt.Errorf("%s already exists inside", sub)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(inner, 0755); err != nil {
		return "", err
	}
	var done []string
	for _, e := range ents {
		if keep != nil && keep(filepath.Join(dir, e.Name())) {
			continue // DLC stays where it is
		}
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(inner, e.Name())); err != nil {
			for i := len(done) - 1; i >= 0; i-- {
				os.Rename(filepath.Join(inner, done[i]), filepath.Join(dir, done[i]))
			}
			os.Remove(inner)
			return "", fmt.Errorf("couldn't move %s: %s", e.Name(), shortErr(err))
		}
		done = append(done, e.Name())
	}
	return inner, nil
}

// unwrapFolder reverses wrapFolder.
func unwrapFolder(dir, inner string) error {
	ents, err := os.ReadDir(inner)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.Rename(filepath.Join(inner, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return os.Remove(inner)
}

func appDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	base := d
	d = filepath.Join(base, "Loadout")
	if _, err := os.Stat(d); err != nil {
		// the app used to be called TitleTag: bring its settings and library along
		if old := filepath.Join(base, "TitleTag"); dirExists(old) {
			os.Rename(old, d)
		}
	}
	os.MkdirAll(d, 0755)
	return d
}

func loadJSON(name string, v any) {
	if b, err := os.ReadFile(filepath.Join(appDir(), name)); err == nil {
		json.Unmarshal(b, v)
	}
}

func saveJSON(name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := filepath.Join(appDir(), name+".tmp")
	if os.WriteFile(tmp, b, 0644) == nil {
		os.Rename(tmp, filepath.Join(appDir(), name))
	}
}

func renameAll(games []*Game, lib *Library, o NameOpts) (ok, failed int, moves []move) {
	for _, g := range games {
		if g.IsDLC {
			continue
		}
		wrap := o.WrapLoose && g.Wrap && !g.IsFile
		if !g.Checked || g.Done || g.NewName == "" || (g.NewName == g.Current && !wrap) {
			continue
		}
		renaming := g.NewName != g.Current
		g.prevPath = g.Path
		if wrap {
			dlc := g.DLC
			inner, err := wrapFolder(g.Path, wrapName(g), func(p string) bool {
				return isDLCPath(p, dlc) || isDLCFolderName(filepath.Base(p))
			})
			if err != nil {
				g.Status = "Failed: " + err.Error()
				failed++
				continue
			}
			moves = append(moves, move{From: g.Path, To: inner, Wrap: true})
			g.Wrap = false
			if g.Meta.SysDir != "" {
				g.Meta.SysDir = filepath.Join(inner, "sce_sys")
			}
			g.Meta.Root = inner
		}
		if renaming {
			dst := filepath.Join(filepath.Dir(g.Path), g.NewName)
			if err := os.Rename(g.Path, dst); err != nil {
				g.Status = "Failed: " + shortErr(err)
				if wrap {
					g.Status = "Files moved into " + wrapName(g) + ", but rename failed: " + shortErr(err)
				}
				failed++
				continue
			}
			moves = append(moves, move{From: g.Path, To: dst})
			if lib != nil && !g.IsFile {
				if e := lib.Games[libKey(g)]; e != nil {
					e.Folder = dst
				}
			}
			for i := range g.DLC {
				if rel, err := filepath.Rel(g.Path, g.DLC[i].Path); err == nil {
					g.DLC[i].Path = filepath.Join(dst, rel)
				}
			}
			for _, p := range []*string{&g.Meta.SysDir, &g.Meta.Root} {
				if *p != "" {
					if rel, err := filepath.Rel(g.Path, *p); err == nil {
						*p = filepath.Join(dst, rel)
					}
				}
			}
			g.Path, g.Current = dst, g.NewName
		}
		g.Done, g.Checked = true, false
		switch {
		case wrap && renaming:
			g.Status = "Renamed + files moved into " + wrapName(g)
		case wrap:
			g.Status = "Files moved into " + wrapName(g)
		default:
			g.Status = "Renamed"
		}
		ok++
	}
	// DLC goes into its game's DLC folder under the DLC naming pattern
	for _, g := range games {
		if !g.IsDLC || !g.Checked || g.Done || g.Parent == nil || !o.MoveDLC || g.Target == "" {
			continue
		}
		par := g.Parent
		cur := g.Path
		if par.prevPath != "" && par.prevPath != par.Path && strings.HasPrefix(cur, par.prevPath+string(os.PathSeparator)) {
			cur = par.Path + cur[len(par.prevPath):]
		}
		dir := filepath.Join(par.Path, "DLC")
		if !dirExists(dir) {
			if err := os.Mkdir(dir, 0755); err != nil {
				g.Status = "Failed: couldn't create the DLC folder: " + shortErr(err)
				failed++
				continue
			}
			moves = append(moves, move{To: dir, Mkdir: true})
		}
		dst := filepath.Join(dir, g.Target)
		if g.DLCInfo.DumpRoot {
			// keep the dump's own folder name, inside a named folder
			if !dirExists(dst) {
				if err := os.Mkdir(dst, 0755); err != nil {
					g.Status = "Failed: " + shortErr(err)
					failed++
					continue
				}
				moves = append(moves, move{To: dst, Mkdir: true})
			}
			dst = filepath.Join(dst, filepath.Base(cur))
		}
		if !strings.EqualFold(cur, dst) {
			if _, err := os.Stat(dst); err == nil {
				g.Status = "Failed: " + filepath.Base(dst) + " already exists in the DLC folder"
				failed++
				continue
			}
		}
		if err := os.Rename(cur, dst); err != nil {
			g.Status = "Failed: " + shortErr(err)
			failed++
			continue
		}
		moves = append(moves, move{From: cur, To: dst})
		if !g.InPlace {
			d := g.DLCInfo
			d.Path = dst
			par.DLC = append(par.DLC, d)
		}
		g.Path, g.Done, g.Checked = dst, true, false
		g.Status = "Done"
		ok++
	}
	// DLC for a game that is only an exFAT image: renamed where it sits
	for _, g := range games {
		if !g.IsDLC || !g.Checked || g.Done || !g.Orphan || g.Target == "" {
			continue
		}
		dst := filepath.Join(filepath.Dir(g.Path), g.Target)
		if !strings.EqualFold(g.Path, dst) {
			if _, err := os.Stat(dst); err == nil {
				g.Status = "Failed: " + g.Target + " already exists"
				failed++
				continue
			}
		}
		if err := os.Rename(g.Path, dst); err != nil {
			g.Status = "Failed: " + shortErr(err)
			failed++
			continue
		}
		moves = append(moves, move{From: g.Path, To: dst})
		g.Path, g.Current, g.Done, g.Checked = dst, g.Target, true, false
		g.Status = "Renamed"
		ok++
	}
	return
}

func undo(moves []move) (ok, failed int) {
	for i := len(moves) - 1; i >= 0; i-- {
		m := moves[i]
		var err error
		if m.Mkdir {
			os.Remove(m.To) // only succeeds if it's empty again
			continue
		} else if m.Wrap {
			err = unwrapFolder(m.From, m.To)
		} else {
			err = os.Rename(m.To, m.From)
		}
		if err != nil {
			failed++
		} else {
			ok++
		}
	}
	return
}

func shortErr(err error) string {
	var le *os.LinkError
	if errors.As(err, &le) {
		err = le.Err
	}
	s := err.Error()
	if strings.Contains(strings.ToLower(s), "used by another process") || strings.Contains(s, "Access is denied") {
		return "folder is in use or locked"
	}
	return s
}

// makeThumb writes a size x size box-filtered copy of an image.
func makeThumb(src, dst string, size int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		y0 := b.Min.Y + y*b.Dy()/size
		y1 := b.Min.Y + (y+1)*b.Dy()/size
		for x := 0; x < size; x++ {
			x0 := b.Min.X + x*b.Dx()/size
			x1 := b.Min.X + (x+1)*b.Dx()/size
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1 || yy == y0; yy++ {
				for xx := x0; xx < x1 || xx == x0; xx++ {
					c := color.NRGBAModel.Convert(img.At(xx, yy)).(color.NRGBA)
					r += uint64(c.R)
					g += uint64(c.G)
					bl += uint64(c.B)
					a += uint64(c.A)
					n++
					if xx >= x1 {
						break
					}
				}
				if yy >= y1 {
					break
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(bl / n), uint8(a / n)})
		}
	}
	w, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer w.Close()
	return png.Encode(w, out)
}

func (lib *Library) KnownNames() map[string]string {
	out := map[string]string{}
	for _, e := range lib.Games {
		if e.TitleID != "" && e.Name != "" {
			out[e.TitleID] = e.Name
		}
	}
	return out
}

func sortName(e *LibEntry) string {
	if e.Name != "" {
		return e.Name
	}
	return filepath.Base(e.Folder)
}

func platformOf(g *Game) string {
	if g.Meta.Platform != "" {
		return g.Meta.Platform
	}
	switch {
	case strings.HasPrefix(g.TitleID, "PPSA"), strings.HasPrefix(g.TitleID, "PPLH"):
		return "PS5"
	case strings.HasPrefix(g.TitleID, "CUSA"):
		return "PS4"
	}
	return ""
}

// ---------- firmware ----------

// fwValue turns "9.60", "09.60", "10.01" into a comparable number (960, 1001).
func fwValue(s string) int {
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(s)
	if m == nil {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil {
			return n * 100
		}
		return -1
	}
	var a, b int
	fmt.Sscanf(m[1], "%d", &a)
	fmt.Sscanf(m[2], "%d", &b)
	if len(m[2]) == 1 {
		b *= 10
	}
	return a*100 + b
}

// needsNewerFW reports whether a PS5 game requires firmware above the console's.
func needsNewerFW(e *LibEntry, console string) bool {
	if e.Platform != "PS5" || console == "" || e.SysVer == "" {
		return false
	}
	c, g := fwValue(console), fwValue(e.SysVer)
	return c >= 0 && g > c
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// movedPath repoints a path saved under the old TitleTag settings folder to
// the same file under Loadout's folder.
func movedPath(p string) string {
	if p == "" || fileExists(p) {
		return p
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return p
	}
	old := filepath.Join(base, "TitleTag") + string(os.PathSeparator)
	if len(p) > len(old) && strings.EqualFold(p[:len(old)], old) {
		if np := filepath.Join(appDir(), p[len(old):]); fileExists(np) {
			return np
		}
	}
	return p
}

// sameDrive reports whether two paths are on the same drive letter (or share).
func sameDrive(a, b string) bool {
	va, vb := filepath.VolumeName(a), filepath.VolumeName(b)
	if va == "" || vb == "" {
		return false
	}
	return strings.EqualFold(va, vb)
}

var backportWord = regexp.MustCompile(`(?i)back[ ._-]?port`)

// isBackported reports whether a game folder has already been backported.
// Backported games carry a "fakelib" folder of replacement libraries beside
// their files; a folder name that says "backport" counts too. Loadout only
// looks: it never adds, changes or removes any of this.
func isBackported(root, folder string) bool {
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			if e.IsDir() && strings.EqualFold(e.Name(), "fakelib") {
				if in, err := os.ReadDir(filepath.Join(root, e.Name())); err == nil && len(in) > 0 {
					return true
				}
			}
		}
	}
	return backportWord.MatchString(filepath.Base(folder)) || backportWord.MatchString(filepath.Base(root))
}
