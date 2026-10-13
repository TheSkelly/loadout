package main

// app.go: all app state and actions, exposed to the UI as a small local
// JSON API. Platform specific bits (window, dialogs, disk space, opening
// Explorer) live in plat_*.go.

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const appVersion = "1.59.0"

//go:embed ui/*
var uiFiles embed.FS

type Settings struct {
	Folder      string
	ShowMissing bool
	Ignored     []string
	Pattern     string
	SafeNames   *bool
	WrapLoose   *bool
	ConsoleFW   string
	ImageOut    string
	View        string            // "grid" or "list"
	RemoveAfter bool              // move the game folder to the Recycle Bin after a verified build
	NukeBig     bool              // a folder too big for the Recycle Bin is deleted for good without asking
	EmuDirs     map[string]string `json:",omitempty"` // emulator folder name -> its games folder, for apps Loadout does not know
	AutoDir     string            `json:",omitempty"` // the autoloader folder on the PS5, when it is not in a usual place
	MoveDLC     *bool             // move loose DLC into its game's folder when renaming
	DLCPattern  string
	PS5IP       string
	PS5Port     int
	PS5Dir      string
	PS5Drive    string            // drive games go to unless picked per game
	PS5Dirs     map[string]string // folder to use on each drive
	SendAfter   bool
	Payloads    []PayloadItem
	ImageDirs   []string // image folders used before, still checked for images
	PayloadPort int      // the PS5's ELF loader, 9021 by default
	AutoShell   bool     // load shsrv by itself when free space is unknown
	SlowSend    bool     // old setting, no longer read
	FastSend    bool     // opt in to the experimental fast receiver
	PlainFTP    bool     // send over FTP only, never through PS5Upload
	SendStreams int      // connections per image with the fast receiver (0 = 4)
	SendAtOnce  int      // images sent at the same time (0 = 2)
	StoreInfo   *bool    // fetch game info and art from the PlayStation Store
	// phone page, alerts, tray
	RemoteOn     bool
	RemotePort   int
	RemotePIN    string
	AlertNtfy    string // ntfy topic or full URL
	AlertDiscord string // Discord webhook URL
	KeepAwake    *bool  // keep the PC awake while jobs run
	CloseToTray  *bool  // closing the window hides it in the tray
	// old fields kept so settings from 1.x load cleanly
	Format     int  `json:",omitempty"`
	AddVersion bool `json:",omitempty"`
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

type BuildJob struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"` // "build" or "remove"
	Name      string `json:"name"`
	Out       string `json:"out"`
	State     string `json:"state"` // queued, building, done, failed, skipped, cancelled
	Note      string `json:"note"`
	Pct       int    `json:"pct"` // 0..1000
	Size      int64  `json:"size"`
	game      *Game
	output    string
	image     string // for remove jobs (and the PS5 path for ps5del jobs)
	remoteDir bool   // ps5del: the PS5 copy is a folder
	gen       int32  // PS5 lane stop counter when this job was queued
	only      string // a ZIP install that must be this one app folder (an update)
	save      *PS5Save
	sums      map[string]FileSum // checksums from this run's build, for the check
	backup    *SaveBackup

	// sending a built image to the PS5 runs alongside the next build
	Send      bool   `json:"send"`
	SendState string `json:"sendState"` // waiting, sending, sent, failed, cancelled
	SendPct   int    `json:"sendPct"`
	SendNote  string `json:"sendNote"`
	Dest      string `json:"dest"` // PS5 folder this job sends to
	DestLabel string `json:"destLabel"`
}

type BuildState struct {
	Running  bool        `json:"running"`  // either lane is busy
	Building bool        `json:"building"` // the build lane is busy
	PS5Busy  bool        `json:"ps5Busy"`  // the PS5 lane is busy
	Jobs     []*BuildJob `json:"jobs"`
	Current  int         `json:"current"`
	Speed    int64       `json:"speed"`
	ETA      int64       `json:"eta"` // seconds
	File     string      `json:"file"`
	Summary  string      `json:"summary"`

	Sending   string `json:"sending"` // game being sent to the PS5 right now
	SendSpeed int64  `json:"sendSpeed"`
	SendSlow  string `json:"sendSlow"` // why a send is crawling, when it is
	SendETA   int64  `json:"sendEta"`
	SendFile  string `json:"sendFile"`
}

type App struct {
	mu          sync.Mutex
	cfg         Settings
	saved       map[string]string
	lastMoves   []move
	lib         *Library
	games       []*Game
	sized       map[string]bool // lib key -> size known
	scanning    bool
	scanErr     string
	sizeJob     int
	sizing      int // games still being measured
	build       BuildState
	cancel      int32
	token       string
	toast       []string
	sent        map[string]SentInfo // image file name -> where it was sent
	ps5info     PS5Info
	saves       SavesInfo
	uploads     map[string][]UploadGroup // PS5 folder -> what each upload put there
	ps5gen      int                      // bumps when the PS5 address changes
	pcdrv       []PCDrive
	autoShellAt time.Time
	ps5q        []*BuildJob // PS5 lane queue
	ps5cond     *sync.Cond
	ps5active   int                     // PS5 jobs running right now
	dropActive  int                     // files dropped on the window that are being streamed to the PS5
	ps5solo     bool                    // a delete or move is running; nothing else may
	sendProg    map[*BuildJob]*sendProg // live uploads, for the combined speed
	ps5st       ps5Stats
	laneNotes   []string
	ps5stop     int32 // goes up each time the PS5 lane is stopped
	ps5busyAt   time.Time
	imgCache    map[string]string
	artKick     chan string
	remote      *remoteServer
	remoteErr   string
	imgCacheKey string
	imgCacheAt  time.Time
}

func newApp() *App {
	a := &App{saved: map[string]string{}, lib: &Library{}, sized: map[string]bool{}}
	loadJSON("settings.json", &a.cfg)
	loadJSON("saved_names.json", &a.saved)
	loadJSON("last_rename.json", &a.lastMoves)
	loadJSON("library.json", a.lib)
	loadJSON("emu-uploads.json", &a.uploads)
	// saved paths from before the rename still point into the old TitleTag folder
	fixed := false
	for _, e := range a.lib.Games {
		if p := movedPath(e.Icon); p != e.Icon {
			e.Icon, fixed = p, true
		}
		if p := movedPath(e.Thumb); p != e.Thumb {
			e.Thumb, fixed = p, true
		}
		if p := movedPath(e.Hero); p != e.Hero {
			e.Hero, fixed = p, true
		}
		if e.Store != nil {
			for i, s := range e.Store.Shots {
				if p := movedPath(s); p != s {
					e.Store.Shots[i], fixed = p, true
				}
			}
		}
	}
	if fixed {
		saveJSON("library.json", a.lib)
	}
	for i := range a.cfg.Payloads {
		a.cfg.Payloads[i].Path = movedPath(a.cfg.Payloads[i].Path)
	}
	if a.lib.Games == nil {
		a.lib.Games = map[string]*LibEntry{}
	}
	if strings.TrimSpace(a.cfg.Pattern) == "" {
		a.cfg.Pattern = defaultPattern
	}
	if strings.TrimSpace(a.cfg.DLCPattern) == "" {
		a.cfg.DLCPattern = defaultDLCPattern
	}
	if a.cfg.Ignored == nil {
		a.cfg.Ignored = append([]string(nil), defaultIgnored...)
	}
	if a.cfg.View == "" {
		a.cfg.View = "grid"
	}
	loadJSON("sent.json", &a.sent)
	if a.sent == nil {
		a.sent = map[string]SentInfo{}
	}
	if a.cfg.PS5Port == 0 {
		a.cfg.PS5Port = 2121
	}
	if a.cfg.PS5Dir == "" {
		a.cfg.PS5Dir = "/data/etaHEN/games"
	}
	if a.cfg.PS5Dirs == nil {
		a.cfg.PS5Dirs = map[string]string{}
	}
	a.cfg.PS5Drive = rootOf(a.cfg.PS5Dir)
	a.ps5cond = sync.NewCond(&a.mu)
	a.sendProg = map[*BuildJob]*sendProg{}
	for i := 0; i < 4; i++ {
		go a.ps5Worker()
	}
	a.artKick = make(chan string, 8)
	go a.artLoop()
	go a.applyRemote()
	setCloseToTray(boolOr(a.cfg.CloseToTray, true))
	go keepAwakeLoop(func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.build.Running && boolOr(a.cfg.KeepAwake, true)
	})
	go a.ps5Loop()
	go a.spaceLoop()
	go a.pcDriveLoop()
	a.cleanTemp()
	b := make([]byte, 16)
	rand.Read(b)
	a.token = hex.EncodeToString(b)
	return a
}

func (a *App) opts() NameOpts {
	// games whose folder is gone but whose exFAT image is here still name their DLC
	names := map[string]string{}
	for k, p := range a.imageIndex() {
		if id := strings.TrimPrefix(k, "id:"); id != k {
			if n := itemFromImage(p).Name; n != "" {
				names[id] = n
			}
		}
	}
	for _, e := range a.lib.Games {
		if e.TitleID != "" && e.Name != "" && names[e.TitleID] != "" {
			names[e.TitleID] = e.Name // the library's name is the proper one
		}
	}
	return NameOpts{Pattern: a.cfg.Pattern, Safe: boolOr(a.cfg.SafeNames, true), WrapLoose: boolOr(a.cfg.WrapLoose, true), MoveDLC: boolOr(a.cfg.MoveDLC, true), DLCPattern: a.cfg.DLCPattern, GameNames: names}
}

func (a *App) outDir() string {
	if a.cfg.ImageOut != "" {
		return a.cfg.ImageOut
	}
	if a.cfg.Folder != "" {
		return filepath.Join(a.cfg.Folder, "exFAT Images")
	}
	return ""
}

// ---------------- state for the UI ----------------

type Item struct {
	Key       string `json:"key"`
	LibKey    string `json:"libKey"`
	Name      string `json:"name"`
	TitleID   string `json:"titleId"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	SDK       string `json:"sdk"`
	ReqFW     string `json:"reqFw"`
	NeedsFW   bool   `json:"needsFw"`
	Backport  bool   `json:"backport"`
	Category  string `json:"category"`
	ContentID string `json:"contentId"`
	Size      int64  `json:"size"`
	Sized     bool   `json:"sized"`
	Path      string `json:"path"`
	Current   string `json:"current"`
	IsFile    bool   `json:"isFile"`
	IsPkg     bool   `json:"isPkg"`
	Present   bool   `json:"present"`
	Cover     bool   `json:"cover"`
	Source    string `json:"source"`
	// rename
	NewName   string `json:"newName"`
	Status    string `json:"status"`
	Checked   bool   `json:"checked"`
	Wrap      bool   `json:"wrap"`
	Done      bool   `json:"done"`
	CanRename bool   `json:"canRename"`
	// build
	ImageName string `json:"imageName"`
	Image     string `json:"image"`
	CanBuild  bool   `json:"canBuild"`
	ExfatOnly bool   `json:"exfatOnly"`
	OnPS5     string `json:"onPs5"`
	CoverKey  string `json:"coverKey"` // what /cover?k= serves for this item
	// storefront
	Hero      bool      `json:"hero"`
	Publisher string    `json:"publisher"`
	Release   string    `json:"release"`
	Genres    []string  `json:"genres"`
	Desc      string    `json:"desc"`
	About     string    `json:"about"`
	AboutURL  string    `json:"aboutUrl"`
	Rating    float64   `json:"rating"`
	Ratings   int       `json:"ratings"`
	Rated     string    `json:"rated"`
	StoreURL  string    `json:"storeUrl"`
	Shots     int       `json:"shots"`
	StoreErr  string    `json:"storeErr"`
	StoreDone bool      `json:"storeDone"`
	OnPS5List []PS5Copy `json:"onPs5List"` // what the last PS5 scan found
	IsDLC     bool      `json:"isDlc"`
	DLCFor    string    `json:"dlcFor"`
	DLC       []DLC     `json:"dlc"`
	Incoming  []DLC     `json:"incoming"`
	Loose     []DLC     `json:"loose"` // DLC sitting in the games folder for a game that is only an image
	InPlace   bool      `json:"inPlace"`
	Note      string    `json:"note"`
	BuildNote string    `json:"buildNote"`
	FirstSeen string    `json:"firstSeen"`
	LastSeen  string    `json:"lastSeen"`
}

type State struct {
	Version     string              `json:"version"`
	Settings    Settings            `json:"settings"`
	Safe        bool                `json:"safe"`
	WrapLoose   bool                `json:"wrapLoose"`
	MoveDLC     bool                `json:"moveDlc"`
	OutDir      string              `json:"outDir"`
	Scanning    bool                `json:"scanning"`
	Sizing      int                 `json:"sizing"`
	Sent        map[string]SentInfo `json:"sent"`
	ScanErr     string              `json:"scanErr"`
	Items       []Item              `json:"items"`
	CanUndo     int                 `json:"canUndo"`
	Build       BuildState          `json:"build"`
	FreeOut     int64               `json:"freeOut"`
	PS5         PS5Info             `json:"ps5"`
	Saves       SavesInfo           `json:"saves"`
	PCDrives    []PCDrive           `json:"pcDrives"`
	Payloads    []PayloadView       `json:"payloads"`
	LoaderPort  int                 `json:"loaderPort"`
	Remote      RemoteInfo          `json:"remote"`
	KeepAwake   bool                `json:"keepAwake"`
	AwakeState  string              `json:"awakeState"`
	AwakeNotes  []string            `json:"awakeNotes"`
	ToTray      bool                `json:"closeToTray"`
	HasTray     bool                `json:"hasTray"`
	Example     string              `json:"example"`
	PatternOK   string              `json:"patternProblem"`
	Presets     []string            `json:"presets"`
	DLCPresets  []string            `json:"dlcPresets"`
	PresetEx    []string            `json:"presetEx"`    // example name for each preset
	DLCPresetEx []string            `json:"dlcPresetEx"` // example name for each DLC preset
	DLCExample  string              `json:"dlcExample"`
	DLCProblem  string              `json:"dlcProblem"`
	Defaults    []string            `json:"defaultIgnored"`
	Toasts      []string            `json:"toasts"`
}

// imageDirsLocked lists every folder that may hold images, current one first:
// the image folder, the default "exFAT Images" folder, folders used before,
// and PS5 drives plugged into this PC.
func (a *App) imageDirsLocked() []string {
	var dirs []string
	add := func(d string) {
		if strings.TrimSpace(d) == "" {
			return
		}
		d = filepath.Clean(d)
		for _, x := range dirs {
			if strings.EqualFold(x, d) {
				return
			}
		}
		dirs = append(dirs, d)
	}
	add(a.outDir())
	if a.cfg.Folder != "" {
		add(filepath.Join(a.cfg.Folder, "exFAT Images"))
	}
	for _, d := range a.cfg.ImageDirs {
		add(d)
	}
	for _, d := range a.pcdrv {
		if d.Ready {
			add(d.Folder)
		}
	}
	return dirs
}

// rememberImageDirLocked keeps a folder images were saved to, so its images
// still show in the library after you switch to another folder.
func (a *App) rememberImageDirLocked(d string) {
	if strings.TrimSpace(d) == "" {
		return
	}
	d = filepath.Clean(d)
	for _, x := range a.cfg.ImageDirs {
		if strings.EqualFold(x, d) {
			return
		}
	}
	a.cfg.ImageDirs = append(a.cfg.ImageDirs, d)
	if len(a.cfg.ImageDirs) > 12 {
		a.cfg.ImageDirs = a.cfg.ImageDirs[len(a.cfg.ImageDirs)-12:]
	}
}

// imageIndex maps "name:<file>" and "id:<title id>" to image paths across
// every image folder. It's cached briefly since the UI polls often.
func (a *App) imageIndex() map[string]string {
	dirs := a.imageDirsLocked()
	key := strings.Join(dirs, "|")
	if a.imgCache != nil && a.imgCacheKey == key && time.Since(a.imgCacheAt) < 4*time.Second {
		return a.imgCache
	}
	idx := map[string]string{}
	for _, out := range dirs {
		ents, err := os.ReadDir(out)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(n), ".exfat") {
				continue
			}
			p := filepath.Join(out, n)
			if _, ok := idx["name:"+strings.ToLower(n)]; !ok {
				idx["name:"+strings.ToLower(n)] = p
			}
			if id := findID(n); id != "" {
				if _, ok := idx["id:"+id]; !ok {
					idx["id:"+id] = p
				}
			}
		}
	}
	a.imgCache, a.imgCacheKey, a.imgCacheAt = idx, key, time.Now()
	return idx
}

var imgVerRe = regexp.MustCompile(`(?i)\bv?(\d{2}\.\d{3}\.\d{3})\b`)

// itemFromImage makes a library card for an image whose game Loadout never saw.
func itemFromImage(p string) Item {
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	id := findID(base)
	title := base
	if id != "" {
		if i := strings.Index(strings.ToUpper(title), id); i > 0 {
			title = strings.TrimRight(title[:i], " [(-_")
		}
	}
	it := Item{Key: "img:" + p, Name: title, TitleID: id, Current: base, Image: p, ExfatOnly: true, Status: "exFAT only", Sized: true}
	if m := imgVerRe.FindStringSubmatch(base); m != nil {
		it.Version = m[1]
	}
	if fi, err := os.Stat(p); err == nil {
		it.Size = fi.Size()
	}
	switch {
	case strings.HasPrefix(id, "PPSA"):
		it.Platform = "PS5"
	case strings.HasPrefix(id, "CUSA"):
		it.Platform = "PS4"
	}
	return it
}

func (a *App) imageNameFor(g *Game) string {
	n := buildName(a.opts(), g.Name, g.TitleID, g.Meta.Version, platformOf(g))
	if n == "" {
		return ""
	}
	return n + ".exfat"
}

func (a *App) stateLocked() State {
	st := State{
		Version: appVersion, Settings: a.cfg, Safe: boolOr(a.cfg.SafeNames, true), WrapLoose: boolOr(a.cfg.WrapLoose, true), MoveDLC: boolOr(a.cfg.MoveDLC, true),
		OutDir: a.outDir(), Scanning: a.scanning, Sizing: a.sizing, ScanErr: a.scanErr, CanUndo: len(a.lastMoves),
		Build: a.build, Presets: patternPresets, Defaults: defaultIgnored, PatternOK: patternProblem(a.cfg.Pattern),
		Example:    buildName(a.opts(), "DRAGON QUEST I & II HD-2D Remake", "PPSA22322", "01.000.000", "PS5"),
		DLCPresets: dlcPresets, DLCProblem: dlcPatternProblem(a.cfg.DLCPattern),
		DLCExample: buildDLCName(a.opts(), safeOr(a.opts(), "RESIDENT EVIL requiem"), "PPSA31246", DLC{Name: "Resident Evil Requiem Deluxe Edition DLC Addon"}),
	}
	st.Toasts, a.toast = a.toast, nil
	for _, p := range patternPresets {
		o := a.opts()
		o.Pattern = p
		st.PresetEx = append(st.PresetEx, buildName(o, "DRAGON QUEST I & II HD-2D Remake", "PPSA22322", "01.000.000", "PS5"))
	}
	for _, p := range dlcPresets {
		o := a.opts()
		o.DLCPattern = p
		st.DLCPresetEx = append(st.DLCPresetEx, buildDLCName(o, safeOr(o, "RESIDENT EVIL requiem"), "PPSA31246", DLC{Name: "Resident Evil Requiem Deluxe Edition DLC Addon", ContentID: "UP0102-PPSA31246_00-RE9DELUXEPACK000"}))
	}
	if st.Build.Jobs == nil {
		st.Build.Jobs = []*BuildJob{}
	}
	st.FreeOut = diskFree(a.outDir())
	st.PS5 = a.ps5info
	st.Saves = a.saves
	if st.Saves.Saves == nil {
		st.Saves.Saves = []PS5Save{}
	}
	if st.Saves.Backups == nil {
		st.Saves.Backups = []SaveBackup{}
	}
	st.PCDrives = a.pcdrv
	st.Payloads = a.payloadViewsLocked()
	st.LoaderPort = a.loaderPortLocked()
	st.Remote = a.remoteInfoLocked()
	st.KeepAwake, st.ToTray, st.HasTray = boolOr(a.cfg.KeepAwake, true), boolOr(a.cfg.CloseToTray, true), hasTray
	st.AwakeState, st.AwakeNotes = cachedAwakeNotes()
	if st.PCDrives == nil {
		st.PCDrives = []PCDrive{}
	}
	if st.PS5.Drives == nil {
		st.PS5.Drives = []PS5Drive{}
	}
	st.Sent = a.sent
	imgs := a.imageIndex()
	seen := map[string]bool{}
	for _, g := range a.games {
		it := Item{Key: g.Path, Name: g.Name, TitleID: g.TitleID, Version: g.Meta.Version, SDK: g.Meta.SDK,
			ReqFW: g.Meta.SysVer, Category: g.Meta.Category, ContentID: g.Meta.ContentID, Path: g.Path, Current: g.Current,
			IsFile: g.IsFile, Present: true, Source: g.Source, NewName: g.NewName, Status: g.Status, Checked: g.Checked,
			Wrap: g.Wrap && boolOr(a.cfg.WrapLoose, true) && !g.IsFile, Done: g.Done, Platform: platformOf(g)}
		it.CanRename = !g.Done && g.NewName != "" && (g.NewName != g.Current || it.Wrap)
		if g.IsDLC {
			it.IsDLC, it.Size, it.Sized, it.Version = true, g.DLCInfo.Size, true, g.DLCInfo.Version
			it.InPlace, it.Note = g.InPlace, g.DLCInfo.Note
			it.CanRename = !g.Done && (g.Parent != nil || g.Orphan) && boolOr(a.cfg.MoveDLC, true) && g.Target != "" && g.Status != "Already named"
			if g.Parent != nil {
				it.DLCFor = g.Parent.displayName()
			}
			st.Items = append(st.Items, it)
			continue
		}
		if !g.IsFile || g.IsPkg {
			if _, err := os.Stat(g.Path); err != nil {
				// the folder was deleted or moved since the last scan
				if e := a.lib.Games[libKey(g)]; e != nil {
					e.Present = false
				}
				continue
			}
		}
		it.DLC = g.DLC
		for _, x := range a.games {
			if x.IsDLC && x.Parent == g && !x.Done && !x.InPlace {
				it.Incoming = append(it.Incoming, x.DLCInfo)
			}
		}
		if !g.IsFile {
			k := libKey(g)
			it.LibKey = k
			seen[k] = true
			if e := a.lib.Games[k]; e != nil {
				it.Size, it.Sized = e.SizeBytes, a.sized[k] || e.SizeBytes > 0
				it.Cover = e.Icon != ""
				it.FirstSeen, it.LastSeen = e.FirstSeen.Format("Jan 2, 2006"), e.LastSeen.Format("Jan 2, 2006 3:04 PM")
				if it.Platform == "" {
					it.Platform = e.Platform
				}
				it.NeedsFW = needsNewerFW(e, a.cfg.ConsoleFW)
				it.Backport = e.Backport
			}
			it.ImageName = a.imageNameFor(g)
			if p := imgs["name:"+strings.ToLower(it.ImageName)]; p != "" {
				it.Image = p
			} else if g.TitleID != "" && imgs["id:"+g.TitleID] != "" {
				it.Image = imgs["id:"+g.TitleID]
			}
			switch {
			case g.Meta.Root == "":
				it.BuildNote = "No game files found inside"
			case g.Name == "":
				it.BuildNote = "Needs a name first"
			default:
				it.CanBuild = true
				if !fileExists(filepath.Join(g.Meta.Root, "eboot.bin")) {
					it.BuildNote = "No eboot.bin found"
				}
			}
		} else if g.IsPkg {
			// a PS4 package: a library game that is one file
			k := libKey(g)
			it.LibKey, it.IsPkg = k, true
			seen[k] = true
			if e := a.lib.Games[k]; e != nil {
				it.Size, it.Sized = e.SizeBytes, true
				it.Cover = e.Icon != ""
				it.FirstSeen, it.LastSeen = e.FirstSeen.Format("Jan 2, 2006"), e.LastSeen.Format("Jan 2, 2006 3:04 PM")
				it.NeedsFW = needsNewerFW(e, a.cfg.ConsoleFW)
			}
			it.BuildNote = "PS4 package"
		} else if fi, err := os.Stat(g.Path); err == nil {
			it.Size, it.Sized = fi.Size(), true
		}
		st.Items = append(st.Items, it)
	}
	{
		libKeys := make([]string, 0, len(a.lib.Games))
		for k := range a.lib.Games {
			libKeys = append(libKeys, k)
		}
		sort.Strings(libKeys) // a fixed order, so rows don't jump around between refreshes
		for _, k := range libKeys {
			e := a.lib.Games[k]
			if seen[k] || e.Present {
				continue
			}
			img := ""
			if e.Image != "" && fileExists(e.Image) {
				img = e.Image
			} else if e.TitleID != "" {
				img = imgs["id:"+e.TitleID]
			}
			if img == "" && !a.cfg.ShowMissing {
				continue
			}
			it := Item{Key: "lib:" + k, LibKey: k, Name: e.Name, TitleID: e.TitleID, Platform: e.Platform, Version: e.Version,
				SDK: e.SDK, ReqFW: e.SysVer, Category: e.Category, ContentID: e.ContentID, Size: e.SizeBytes, Sized: true,
				Path: e.Folder, Current: filepath.Base(e.Folder), Present: false, Cover: e.Icon != "",
				FirstSeen: e.FirstSeen.Format("Jan 2, 2006"), LastSeen: e.LastSeen.Format("Jan 2, 2006 3:04 PM"), Status: "Missing", Backport: e.Backport}
			if it.Name == "" {
				it.Name = it.Current
			}
			if img != "" {
				it.Image, it.ExfatOnly, it.Status = img, true, "exFAT only"
			}
			it.DLC = e.DLC
			st.Items = append(st.Items, it)
		}
	}
	{
		used := map[string]bool{}
		for _, it := range st.Items {
			if it.Image != "" {
				used[strings.ToLower(it.Image)] = true
			}
		}
		imgKeys := make([]string, 0, len(imgs))
		for k := range imgs {
			imgKeys = append(imgKeys, k)
		}
		sort.Strings(imgKeys)
		for _, k := range imgKeys {
			p := imgs[k]
			if strings.HasPrefix(k, "name:") && !used[strings.ToLower(p)] {
				used[strings.ToLower(p)] = true
				it := itemFromImage(p)
				// borrow cover and details from the library if we know the game
				for lk, e := range a.lib.Games {
					if e.TitleID != "" && e.TitleID == it.TitleID {
						it.LibKey, it.Cover, it.SDK, it.ReqFW, it.ContentID = lk, e.Icon != "", e.SDK, e.SysVer, e.ContentID
						it.Backport = e.Backport
						if e.Name != "" {
							it.Name = e.Name
						}
						break
					}
				}
				st.Items = append(st.Items, it)
			}
		}
	}
	// DLC whose game is only here as an exFAT image: the folder is gone, so the
	// DLC can't move into it, but the game still shows that it has DLC
	{
		byID := map[string]int{}
		for i := range st.Items {
			if it := &st.Items[i]; !it.IsDLC && !it.IsFile && it.TitleID != "" && it.Image != "" {
				if _, dup := byID[it.TitleID]; !dup {
					byID[it.TitleID] = i
				}
			}
		}
		for _, g := range a.games {
			if !g.IsDLC || g.Parent != nil || g.TitleID == "" {
				continue
			}
			gi, ok := byID[g.TitleID]
			if !ok {
				continue
			}
			game := &st.Items[gi]
			d := g.DLCInfo
			if d.Path == "" {
				d.Path = g.Path
			}
			game.Loose = append(game.Loose, d)
			name := game.Name
			if name == "" {
				name = game.Current
			}
			for i := range st.Items {
				if x := &st.Items[i]; x.IsDLC && x.Key == g.Path {
					x.DLCFor = name
					if strings.Contains(x.Status, "isn't in this folder") {
						x.Status = "DLC for " + name + ". That game is an exFAT image now, so the DLC stays where it is"
					}
				}
			}
		}
	}
	scanned := a.ps5info.Online && a.ps5info.Scanned
	for i := range st.Items {
		it := &st.Items[i]
		if e := a.lib.Games[it.LibKey]; e != nil && it.LibKey != "" && !it.IsDLC {
			it.Hero = e.Hero != "" && fileExists(e.Hero)
			if si := e.Store; si != nil {
				it.StoreDone, it.StoreErr = true, si.Err
				it.Publisher, it.Release, it.Genres, it.Desc = si.Publisher, si.Release, enGenres(si.Genres), si.Desc
				it.About, it.AboutURL = si.About, si.AboutURL
				it.Rating, it.Ratings, it.Rated, it.StoreURL, it.Shots = si.Rating, si.Ratings, si.Rated, si.URL, len(si.Shots)
			}
		}
		// no cover from the game files? use the one the PS5 gave us
		if it.Cover {
			it.CoverKey = it.LibKey
		} else if it.TitleID != "" && fileExists(ps5IconPath(it.TitleID)) {
			it.Cover, it.CoverKey = true, "id:"+it.TitleID
		}
		if scanned {
			// the PS5 scan is the truth: it also catches games copied some other way
			if cs := a.ps5info.Games[it.TitleID]; it.TitleID != "" && len(cs) > 0 {
				it.OnPS5List = cs
				it.OnPS5 = cs[0].Drive + ": " + cs[0].Path
			}
			continue
		}
		if it.Image != "" {
			if si, ok := a.sent[strings.ToLower(filepath.Base(it.Image))]; ok {
				it.OnPS5 = si.Dir
			}
		}
	}
	if st.Items == nil {
		st.Items = []Item{}
	}
	return st
}

func (a *App) notify(format string, args ...any) {
	a.toast = append(a.toast, fmt.Sprintf(format, args...))
}

// ---------------- actions ----------------

func (a *App) replanLocked() { plan(a.games, a.opts()) }

func (a *App) startScan(folder string) error {
	a.mu.Lock()
	if folder == "" {
		folder = a.cfg.Folder
	}
	folder = strings.TrimSpace(folder)
	if folder == "" {
		a.mu.Unlock()
		return errors.New("Pick your games folder first.")
	}
	if st, err := os.Stat(folder); err != nil || !st.IsDir() {
		a.mu.Unlock()
		return fmt.Errorf("That folder doesn't exist: %s", folder)
	}
	if a.build.Building {
		a.mu.Unlock()
		return errors.New("Wait for the build to finish before rescanning.")
	}
	if a.scanning {
		a.mu.Unlock()
		return nil
	}
	a.cfg.Folder = folder
	saveJSON("settings.json", a.cfg)
	a.scanning, a.scanErr = true, ""
	known, ignored, saved := a.lib.KnownNames(), append([]string(nil), a.cfg.Ignored...), copyMap(a.saved)
	a.mu.Unlock()

	go func() {
		games, err := scan(folder, saved, known, ignored)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.scanning = false
		if err != nil {
			a.scanErr = err.Error()
			return
		}
		a.games = games
		updateLibrary(a.lib, folder, games)
		saveJSON("library.json", a.lib)
		a.replanLocked()
		a.startSizesLocked()
	}()
	return nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// startSizesLocked measures game folders in the background. Sizes from
// earlier runs are shown straight away and quietly refreshed; games with
// no size yet go first. Three games are measured at a time.
func (a *App) startSizesLocked() {
	a.sizeJob++
	job := a.sizeJob
	type todo struct {
		k, p  string
		known bool
	}
	var list []todo
	for k, e := range a.lib.Games {
		if e.Pkg {
			a.sized[k] = true // a package's size is just its file size
			continue
		}
		if e.Present && !a.sized[k] {
			list = append(list, todo{k, e.Folder, e.SizeBytes > 0})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].known != list[j].known {
			return !list[i].known
		}
		return list[i].p < list[j].p
	})
	a.sizing = len(list)
	work := make(chan todo)
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				n := folderSize(t.p)
				a.mu.Lock()
				if job == a.sizeJob {
					if e := a.lib.Games[t.k]; e != nil && e.Folder == t.p {
						e.SizeBytes = n
						a.sized[t.k] = true
					}
					a.sizing--
				}
				a.mu.Unlock()
			}
		}()
	}
	go func() {
		for _, t := range list {
			a.mu.Lock()
			stale := job != a.sizeJob
			a.mu.Unlock()
			if stale {
				break
			}
			work <- t
		}
		close(work)
		wg.Wait()
		a.mu.Lock()
		if job == a.sizeJob {
			a.sizing = 0
			saveJSON("library.json", a.lib)
		}
		a.mu.Unlock()
	}()
}

func (a *App) gameByKey(k string) *Game {
	for _, g := range a.games {
		if g.Path == k {
			return g
		}
	}
	return nil
}

func (a *App) setChecked(keys []string, c bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	wrapOn := boolOr(a.cfg.WrapLoose, true)
	for _, k := range keys {
		if g := a.gameByKey(k); g != nil && g.IsDLC {
			g.Checked = c && !g.Done && (g.Parent != nil || g.Orphan) && boolOr(a.cfg.MoveDLC, true) && g.Target != "" && g.Status != "Already named"
			g.autoOff = false
		} else if g != nil {
			g.Checked = c && !g.Done && g.NewName != "" && (g.NewName != g.Current || (g.Wrap && wrapOn && !g.IsFile))
			g.autoOff = false
		}
	}
}

func (a *App) rename() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.build.Building {
		return "", errors.New("Wait for the build to finish first.")
	}
	ok, failed, moves := renameAll(a.games, a.lib, a.opts())
	if len(moves) > 0 {
		a.lastMoves = moves
		saveJSON("last_rename.json", moves)
		saveJSON("library.json", a.lib)
	}
	if ok == 0 && failed == 0 {
		return "", errors.New("Nothing checked to rename.")
	}
	msg := fmt.Sprintf("Renamed %d item(s).", ok)
	if failed > 0 {
		msg += fmt.Sprintf(" %d failed, usually because a folder is open in Explorer or another program.", failed)
	}
	return msg, nil
}

func (a *App) undo() (string, error) {
	a.mu.Lock()
	if len(a.lastMoves) == 0 {
		a.mu.Unlock()
		return "", errors.New("Nothing to undo.")
	}
	ok, failed := undo(a.lastMoves)
	a.lastMoves = nil
	os.Remove(filepath.Join(appDir(), "last_rename.json"))
	a.mu.Unlock()
	a.startScan("")
	msg := fmt.Sprintf("Put back %d change(s).", ok)
	if failed > 0 {
		msg += fmt.Sprintf(" %d couldn't be undone.", failed)
	}
	return msg, nil
}

func (a *App) setName(key, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.gameByKey(key)
	if g == nil {
		return errors.New("That game isn't in the list anymore. Rescan and try again.")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("Type a name.")
	}
	g.Name, g.Source, g.Status = name, "manual", ""
	if g.TitleID != "" {
		a.saved[g.TitleID] = name
		saveJSON("saved_names.json", a.saved)
	}
	if e := a.lib.Games[libKey(g)]; e != nil && !g.IsFile {
		e.Name = name
		saveJSON("library.json", a.lib)
	}
	a.replanLocked()
	g.Checked = g.NewName != "" && g.NewName != g.Current
	return nil
}

func (a *App) clearMissing() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	imgs := a.imageIndex()
	for k, e := range a.lib.Games {
		if e.Present {
			continue
		}
		if (e.Image != "" && fileExists(e.Image)) || (e.TitleID != "" && imgs["id:"+e.TitleID] != "") {
			continue // exFAT-only games stay in the library
		}
		delete(a.lib.Games, k)
		n++
	}
	saveJSON("library.json", a.lib)
	return n
}

// ---------------- build queue ----------------

func (a *App) startBuild(keys, sendKeys []string, dests map[string]string, overwrite bool) error {
	send := map[string]bool{}
	for _, k := range sendKeys {
		send[k] = true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.build.Building {
		return errors.New("A build is already running.")
	}
	out := a.outDir()
	if out == "" {
		return errors.New("Pick an output folder for images first.")
	}
	// images the PS5 lane is still sending can't be rebuilt underneath it
	sending := map[string]bool{}
	for _, j := range a.ps5q {
		sending[strings.ToLower(filepath.Base(j.image+j.output))] = true
	}
	for _, j := range a.build.Jobs {
		if j.SendState == "sending" || j.State == "sending" {
			sending[strings.ToLower(filepath.Base(j.image+j.output))] = true
		}
	}
	var jobs []*BuildJob
	skippedSending := 0
	for _, k := range keys {
		g := a.gameByKey(k)
		if g == nil || g.IsFile || g.IsDLC || g.Meta.Root == "" || g.Name == "" {
			continue
		}
		if sending[strings.ToLower(a.imageNameFor(g))] {
			skippedSending++
			continue
		}
		cleanOut := strings.ToLower(filepath.Clean(out) + string(os.PathSeparator))
		if strings.HasPrefix(cleanOut, strings.ToLower(filepath.Clean(g.Path)+string(os.PathSeparator))) {
			return fmt.Errorf("The image folder is inside \"%s\". Pick an output folder outside your game folders.", g.Current)
		}
		var size int64
		if e := a.lib.Games[libKey(g)]; e != nil {
			size = e.SizeBytes
		}
		j := &BuildJob{Key: k, Kind: "build", Name: g.Name, Out: a.imageNameFor(g), State: "queued", Size: size, game: g,
			Send: send[k] && strings.TrimSpace(a.cfg.PS5IP) != ""}
		if j.Send {
			j.Dest, j.DestLabel = a.destLocked(dests[k])
			j.SendState = "waiting"
		}
		jobs = append(jobs, j)
	}
	if len(jobs) == 0 {
		if skippedSending > 0 {
			return errors.New("That image is being sent to the PS5 right now. Build it again once the send finishes.")
		}
		return errors.New("None of those games can be built yet.")
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return fmt.Errorf("Couldn't create the output folder: %v", err)
	}
	a.cfg.ImageOut = out
	a.rememberImageDirLocked(out)
	saveJSON("settings.json", a.cfg)
	atomic.StoreInt32(&a.cancel, 0)
	a.addJobsLocked(jobs)
	a.build.Building = true
	go a.runBuild(jobs, out, overwrite, a.cfg.RemoveAfter)
	return nil
}

// startRemove verifies each game's existing image, then recycles its folder.
func (a *App) startRemove(keys []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.build.Building {
		return errors.New("Wait for the current build to finish first.")
	}
	imgs := a.imageIndex()
	var jobs []*BuildJob
	for _, k := range keys {
		g := a.gameByKey(k)
		if g == nil || g.IsFile || g.IsDLC {
			continue
		}
		img := imgs["name:"+strings.ToLower(a.imageNameFor(g))]
		if img == "" && g.TitleID != "" {
			img = imgs["id:"+g.TitleID]
		}
		if img == "" {
			continue
		}
		var size int64
		if e := a.lib.Games[libKey(g)]; e != nil {
			size = e.SizeBytes
		}
		jobs = append(jobs, &BuildJob{Key: k, Kind: "remove", Name: g.Name, Out: filepath.Base(img), State: "queued", Size: size, game: g, image: img})
	}
	if len(jobs) == 0 {
		return errors.New("Those games don't have an image yet, so their folders were left alone.")
	}
	atomic.StoreInt32(&a.cancel, 0)
	a.addJobsLocked(jobs)
	a.build.Building = true
	go a.runBuild(jobs, a.outDir(), false, true)
	return nil
}

// removeSource checks img against the game folder and, only if it matches
// exactly, moves the folder to the Recycle Bin. It returns a short note.
func (a *App) removeSource(j *BuildJob, img string) (removed bool, note string, mismatch bool) {
	g := j.game
	if len(g.DLC) > 0 {
		return false, fmt.Sprintf("Folder kept: it also holds %d DLC, which isn't in the image", len(g.DLC)), false
	}
	if err := removableRoot(g.Path, g.Meta.Root); err != nil {
		return false, "Folder kept: " + err.Error(), false
	}
	a.mu.Lock()
	j.State, j.Pct, j.Note = "verifying", 0, ""
	a.mu.Unlock()
	start := time.Now()
	var last time.Time
	err := VerifyExfatSums(img, g.Meta.Root, j.sums, func(done, total int64, cur string) {
		if time.Since(last) < 120*time.Millisecond || total <= 0 {
			return
		}
		last = time.Now()
		speed := float64(done) / time.Since(start).Seconds()
		a.mu.Lock()
		j.Pct = int(done * 1000 / total)
		a.build.Speed, a.build.File = int64(speed), filepath.Base(cur)
		if speed > 0 {
			a.build.ETA = int64(float64(total-done) / speed)
		}
		a.mu.Unlock()
	}, func() bool { return atomic.LoadInt32(&a.cancel) != 0 })
	if errors.Is(err, ErrCancelled) {
		return false, "Folder kept (stopped before the check finished)", false
	}
	if err != nil {
		return false, "Folder kept, the image check failed: " + err.Error(), true
	}
	a.mu.Lock()
	j.State, j.Pct = "removing", 1000
	a.mu.Unlock()
	a.mu.Lock()
	noAsk := a.cfg.NukeBig
	a.mu.Unlock()
	kept, err := recycle(g.Path, noAsk)
	if err != nil {
		return false, "Image checked OK, but the folder was kept: " + err.Error(), false
	}
	if kept {
		return false, "Image checked OK, folder kept (you chose not to delete it)", false
	}
	a.mu.Lock()
	if e := a.lib.Games[libKey(g)]; e != nil {
		e.Present, e.Image = false, img
	}
	for i, x := range a.games {
		if x == g {
			a.games = append(a.games[:i], a.games[i+1:]...)
			break
		}
	}
	saveJSON("library.json", a.lib)
	a.mu.Unlock()
	if noAsk {
		return true, "Image checked, folder deleted for good", false
	}
	return true, "Image checked, folder moved to the Recycle Bin", false
}

func (a *App) runBuild(jobs []*BuildJob, out string, overwrite, removeAfter bool) {
	ok, failed, skipped, removed := 0, 0, 0, 0
	start := time.Now()
	cancelled := func() bool { return atomic.LoadInt32(&a.cancel) != 0 }
	// finished images go straight to the PS5 lane, which sends while we build on
	queueSend := func(j *BuildJob, img string) {
		a.mu.Lock()
		if !j.Send || j.SendState != "waiting" { // not asked for, or stopped meanwhile
			a.mu.Unlock()
			return
		}
		j.output = img
		a.enqueuePS5Locked(j)
		a.mu.Unlock()
	}
	set := func(j *BuildJob, state, note string) {
		a.mu.Lock()
		j.State, j.Note = state, note
		a.mu.Unlock()
	}
	for _, j := range jobs {
		a.mu.Lock()
		for i, x := range a.build.Jobs {
			if x == j {
				a.build.Current = i
			}
		}
		a.mu.Unlock()
		if cancelled() {
			set(j, "cancelled", "")
			continue
		}
		if j.Kind == "remove" {
			gone, note, bad := a.removeSource(j, j.image)
			state := "done"
			if bad {
				state = "failed"
				failed++
			} else if gone {
				removed++
				ok++
			}
			set(j, state, note)
			continue
		}
		dst := filepath.Join(out, j.Out)
		if _, err := os.Stat(dst); err == nil && !overwrite {
			set(j, "skipped", "Image already exists")
			skipped++
			queueSend(j, dst) // already built, but still send it if asked
			continue
		}
		set(j, "building", "Scanning files")
		label := j.game.TitleID
		if label == "" {
			label = "PS5GAME"
		}
		plan, err := PlanExfat(j.game.Meta.Root, label)
		if err != nil {
			set(j, "failed", err.Error())
			failed++
			continue
		}
		if free := diskFree(out); free >= 0 && free < plan.TotalBytes+(256<<20) {
			set(j, "failed", fmt.Sprintf("Not enough space: needs %s, %s free", humanSize(plan.TotalBytes), humanSize(free)))
			failed++
			continue
		}
		tmp := dst + ".building"
		plan.SameDisk = sameDrive(j.game.Meta.Root, out)
		jobStart := time.Now()
		var last time.Time
		err = plan.Write(tmp, func(done, total int64, cur string) {
			if time.Since(last) < 120*time.Millisecond && done < total {
				return
			}
			last = time.Now()
			speed := float64(done) / time.Since(jobStart).Seconds()
			a.mu.Lock()
			j.Pct = int(done * 1000 / total)
			j.Note = ""
			j.Size = plan.TotalBytes
			a.build.Speed = int64(speed)
			if speed > 0 {
				a.build.ETA = int64(float64(total-done) / speed)
			}
			a.build.File = filepath.Base(cur)
			a.mu.Unlock()
		}, cancelled)
		if err == nil {
			os.Remove(dst)
			err = os.Rename(tmp, dst)
		}
		switch {
		case errors.Is(err, ErrCancelled):
			set(j, "cancelled", "")
		case err != nil:
			set(j, "failed", err.Error())
			failed++
		default:
			a.mu.Lock()
			j.State, j.Pct, j.output = "done", 1000, dst
			j.sums = plan.Sums
			j.Note = fmt.Sprintf("%s in %s", humanSize(plan.TotalBytes), fmtDur(time.Since(jobStart)))
			built := j.Note
			a.mu.Unlock()
			ok++
			if !removeAfter {
				queueSend(j, dst)
			}
			if removeAfter {
				// the image is checked first; it only goes to the PS5 once it passes
				gone, note, bad := a.removeSource(j, dst)
				state := "done"
				if bad {
					state = "failed"
					a.mu.Lock()
					if j.Send && j.SendState == "waiting" {
						j.SendState, j.SendNote = "cancelled", "The image failed its check, so it wasn't sent"
					}
					a.mu.Unlock()
				} else {
					queueSend(j, dst)
				}
				if gone {
					removed++
				}
				set(j, state, built+". "+note)
			}
		}
	}
	// jobs that never got built can't be sent
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range jobs {
		if j.Send && j.SendState == "waiting" && (j.State == "failed" || j.State == "cancelled") {
			j.SendState = "cancelled"
			j.SendNote = "Not built, so not sent"
		}
	}
	a.build.Building, a.build.File, a.build.ETA, a.build.Speed = false, "", 0, 0
	s := fmt.Sprintf("Built %d image(s) in %s.", ok, fmtDur(time.Since(start)))
	if len(jobs) > 0 && jobs[0].Kind == "remove" {
		s = fmt.Sprintf("Moved %d game folder(s) to the Recycle Bin.", removed)
	} else if removed > 0 {
		s += fmt.Sprintf(" %d game folder(s) moved to the Recycle Bin.", removed)
	}
	if skipped > 0 {
		s += fmt.Sprintf(" %d skipped (already built).", skipped)
	}
	if failed > 0 {
		s += fmt.Sprintf(" %d failed.", failed)
	}
	if cancelled() {
		s = "Building stopped. " + s
	}
	a.finishLaneLocked(s)
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ---------------- HTTP ----------------

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fsSub(uiFiles, "ui")
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("/cover", func(w http.ResponseWriter, r *http.Request) {
		k := r.URL.Query().Get("k")
		a.mu.Lock()
		e := a.lib.Games[k]
		var p string
		if e != nil {
			p = e.Icon
		}
		a.mu.Unlock()
		if id := strings.TrimPrefix(k, "id:"); id != k && gameID(id) == id {
			p = ps5IconPath(id) // icon pulled from the PS5
		}
		if id := strings.TrimPrefix(k, "home:"); id != k && gameID(id) == id {
			p = homeIconPath(id) // a home screen tile's picture
		}
		if ek := strings.TrimPrefix(k, "emu:"); ek != k && len(ek) == 20 && plainKey(ek) == ek {
			p = emuCoverPath(ek) // cover an emulator cached for one of its games
		}
		if p == "" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// store art can be JPEG even though it's saved as .png
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "max-age=300")
		w.Write(b)
	})
	mux.HandleFunc("/art", func(w http.ResponseWriter, r *http.Request) {
		k, f := r.URL.Query().Get("k"), r.URL.Query().Get("f")
		a.mu.Lock()
		var p string
		if e := a.lib.Games[k]; e != nil {
			if f == "hero" {
				p = e.Hero
			} else if strings.HasPrefix(f, "shot") && e.Store != nil {
				if n, err := strconv.Atoi(f[4:]); err == nil && n >= 0 && n < len(e.Store.Shots) {
					p = e.Store.Shots[n]
				}
			}
		}
		a.mu.Unlock()
		if p == "" || !strings.HasPrefix(filepath.Clean(p), filepath.Clean(artDir())) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=600")
		http.ServeFile(w, r, p)
	})
	a.dropRoutes(mux)
	api := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != a.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			res, err := fn(r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": res})
		})
	}
	body := func(r *http.Request, v any) {
		json.NewDecoder(r.Body).Decode(v)
	}
	api("/api/state", func(r *http.Request) (any, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.stateLocked(), nil
	})
	api("/api/scan", func(r *http.Request) (any, error) {
		var b struct{ Folder string }
		body(r, &b)
		return nil, a.startScan(b.Folder)
	})
	api("/api/settings", func(r *http.Request) (any, error) {
		var b map[string]json.RawMessage
		body(r, &b)
		a.mu.Lock()
		defer a.mu.Unlock()
		str := func(k string, dst *string) {
			if v, ok := b[k]; ok {
				json.Unmarshal(v, dst)
			}
		}
		flag := func(k string) *bool {
			if v, ok := b[k]; ok {
				var x bool
				json.Unmarshal(v, &x)
				return &x
			}
			return nil
		}
		str("pattern", &a.cfg.Pattern)
		str("dlcPattern", &a.cfg.DLCPattern)
		str("ps5Ip", &a.cfg.PS5IP)
		oldIP, oldPort := a.cfg.PS5IP, a.cfg.PS5Port
		if v, ok := b["ps5Dir"]; ok {
			var d string
			json.Unmarshal(v, &d)
			if d = strings.TrimSpace(d); d != "" {
				d = path.Clean("/" + d)
				a.cfg.PS5Dir, a.cfg.PS5Drive = d, rootOf(d)
				a.cfg.PS5Dirs[rootOf(d)] = d
				a.refreshDirsLocked()
			}
		}
		if v, ok := b["ps5Drive"]; ok {
			var r string
			json.Unmarshal(v, &r)
			if r = rootOf(r); r != "" {
				a.cfg.PS5Dirs[rootOf(a.cfg.PS5Dir)] = a.cfg.PS5Dir
				a.cfg.PS5Dir, a.cfg.PS5Drive = a.dirForLocked(r), r
			}
		}
		defer func() {
			go a.refreshPCDrives()
			if a.cfg.PS5IP != oldIP || a.cfg.PS5Port != oldPort {
				a.ps5gen++
				a.ps5info = PS5Info{}
				go a.refreshPS5()
			}
		}()
		if v, ok := b["ps5Port"]; ok {
			json.Unmarshal(v, &a.cfg.PS5Port)
			if a.cfg.PS5Port <= 0 || a.cfg.PS5Port > 65535 {
				a.cfg.PS5Port = 2121
			}
		}
		if v := flag("sendAfter"); v != nil {
			a.cfg.SendAfter = *v
		}
		if v := flag("fastSend"); v != nil {
			a.cfg.FastSend = *v
		}
		if v := flag("plainFTP"); v != nil {
			a.cfg.PlainFTP = *v
		}
		for k, p := range map[string]*int{"sendStreams": &a.cfg.SendStreams, "sendAtOnce": &a.cfg.SendAtOnce} {
			if v, ok := b[k]; ok {
				json.Unmarshal(v, p)
				if *p < 1 || *p > 4 {
					*p = 0
				}
			}
		}
		a.ps5cond.Broadcast()
		if v := flag("autoShell"); v != nil {
			a.cfg.AutoShell = *v
		}
		if v := flag("keepAwake"); v != nil {
			a.cfg.KeepAwake = v
		}
		if v := flag("closeToTray"); v != nil {
			a.cfg.CloseToTray = v
			setCloseToTray(*v)
		}
		str("alertNtfy", &a.cfg.AlertNtfy)
		str("alertDiscord", &a.cfg.AlertDiscord)
		oldOn, oldRPort := a.cfg.RemoteOn, a.cfg.RemotePort
		if v := flag("remoteOn"); v != nil {
			a.cfg.RemoteOn = *v
		}
		if v, ok := b["remotePin"]; ok {
			var pin string
			json.Unmarshal(v, &pin)
			a.cfg.RemotePIN = strings.TrimSpace(pin)
		}
		if v, ok := b["remotePort"]; ok {
			json.Unmarshal(v, &a.cfg.RemotePort)
			if a.cfg.RemotePort < 1024 || a.cfg.RemotePort > 65535 {
				a.cfg.RemotePort = defaultRemotePort
			}
		}
		if _, pinSet := b["remotePin"]; a.cfg.RemoteOn != oldOn || a.cfg.RemotePort != oldRPort || (pinSet && a.cfg.RemoteOn && a.remote == nil) {
			go a.applyRemote()
		}
		if v := flag("storeInfo"); v != nil {
			a.cfg.StoreInfo = v
			if *v {
				artTriedMu.Lock()
				artTried = map[string]bool{}
				artTriedMu.Unlock()
				select {
				case a.artKick <- "":
				default:
				}
			}
		}
		if v, ok := b["payloadPort"]; ok {
			json.Unmarshal(v, &a.cfg.PayloadPort)
			if a.cfg.PayloadPort <= 0 || a.cfg.PayloadPort > 65535 {
				a.cfg.PayloadPort = defaultLoaderPort
			}
		}
		str("consoleFw", &a.cfg.ConsoleFW)
		a.rememberImageDirLocked(a.outDir())
		str("imageOut", &a.cfg.ImageOut)
		str("view", &a.cfg.View)
		if v := flag("safe"); v != nil {
			a.cfg.SafeNames = v
		}
		if v := flag("wrapLoose"); v != nil {
			a.cfg.WrapLoose = v
		}
		if v := flag("moveDlc"); v != nil {
			a.cfg.MoveDLC = v
		}
		if v := flag("nukeBig"); v != nil {
			a.cfg.NukeBig = *v
		}
		if v := flag("removeAfter"); v != nil {
			a.cfg.RemoveAfter = *v
		}
		if v := flag("showMissing"); v != nil {
			a.cfg.ShowMissing = *v
		}
		rescan := false
		if v, ok := b["ignored"]; ok {
			var list []string
			json.Unmarshal(v, &list)
			if list == nil {
				list = []string{}
			}
			a.cfg.Ignored = list
			rescan = true
		}
		a.cfg.Pattern = strings.TrimSpace(a.cfg.Pattern)
		if a.cfg.Pattern == "" {
			a.cfg.Pattern = defaultPattern
		}
		a.cfg.DLCPattern = strings.TrimSpace(a.cfg.DLCPattern)
		if a.cfg.DLCPattern == "" {
			a.cfg.DLCPattern = defaultDLCPattern
		}
		saveJSON("settings.json", a.cfg)
		a.replanLocked()
		if rescan && a.cfg.Folder != "" {
			go a.startScan("")
		}
		return nil, nil
	})
	api("/api/check", func(r *http.Request) (any, error) {
		var b struct {
			Keys    []string
			Checked bool
		}
		body(r, &b)
		a.setChecked(b.Keys, b.Checked)
		return nil, nil
	})
	api("/api/rename", func(r *http.Request) (any, error) { return a.rename() })
	api("/api/undo", func(r *http.Request) (any, error) { return a.undo() })
	api("/api/setname", func(r *http.Request) (any, error) {
		var b struct{ Key, Name string }
		body(r, &b)
		return nil, a.setName(b.Key, b.Name)
	})
	api("/api/ignore", func(r *http.Request) (any, error) {
		var b struct{ Name string }
		body(r, &b)
		a.mu.Lock()
		if b.Name != "" && !isIgnored(b.Name, a.cfg.Ignored) {
			a.cfg.Ignored = append(a.cfg.Ignored, b.Name)
			saveJSON("settings.json", a.cfg)
		}
		a.mu.Unlock()
		return nil, a.startScan("")
	})
	api("/api/build", func(r *http.Request) (any, error) {
		var b struct {
			Keys      []string
			Send      []string          // games to send to the PS5 once built
			Dests     map[string]string // game key -> PS5 drive root
			Overwrite bool
		}
		body(r, &b)
		return nil, a.startBuild(b.Keys, b.Send, b.Dests, b.Overwrite)
	})
	api("/api/remove", func(r *http.Request) (any, error) {
		var b struct{ Keys []string }
		body(r, &b)
		return nil, a.startRemove(b.Keys)
	})
	api("/api/send", func(r *http.Request) (any, error) {
		var b struct{ Items []sendItem }
		body(r, &b)
		return nil, a.startSend(b.Items)
	})
	api("/api/ps5/recvtest", func(r *http.Request) (any, error) {
		host, port, dir := a.ps5()
		if host == "" {
			return nil, errors.New("Set your PS5's IP address first.")
		}
		a.mu.Lock()
		busy, loader, streams := a.build.PS5Busy, a.loaderPortLocked(), a.cfg.SendStreams
		a.mu.Unlock()
		if busy {
			return nil, errors.New("Wait for the current PS5 job to finish first.")
		}
		if streams == 0 {
			streams = defaultStreams
		}
		res := map[string]any{"ok": false}
		was := recvPing(host)
		if !was {
			if err := sendPayload(host, loader, recvELF); err != nil {
				res["msg"] = "The receiver isn't running and Loadout couldn't load it: " + err.Error() + ". Sends will use plain FTP."
				return res, nil
			}
			up := false
			for i := 0; i < 20 && !up; i++ {
				time.Sleep(300 * time.Millisecond)
				up = recvPing(host)
			}
			if !up {
				res["msg"] = fmt.Sprintf("Loadout sent the receiver to the PS5's loader (port %d), but it didn't start. Sends will use plain FTP. Did the PS5 show a pop-up or an error?", loader)
				return res, nil
			}
			recvMu.Lock()
			delete(recvFailed, host)
			recvMu.Unlock()
		}
		state := "The receiver was already running."
		if !was {
			state = "The receiver wasn't running, so Loadout loaded it just now."
		}
		f, err := ftpDial(host, port)
		if err != nil {
			res["msg"] = state + " The PS5's FTP isn't answering though, and sends need it too: " + err.Error()
			return res, nil
		}
		defer f.Close()
		// three measurements, so the result says where the time goes
		line := func(label string, bps float64, sent int64, err error, secs float64) string {
			if err == nil {
				return fmt.Sprintf("%s: %.1f MB/s.", label, bps/1e6)
			}
			if errors.Is(err, errRecvDisk) {
				return label + ": the PS5 couldn't write the test file."
			}
			if sent > 8<<20 {
				return fmt.Sprintf("%s: the PS5 took %.1f MB/s off the network but wrote none of it to the drive in time, so the drive write is what's stuck.", label, float64(sent)/secs/1e6)
			}
			return fmt.Sprintf("%s: almost nothing got through (%s in %.0f s), so the network link itself is very slow right now.", label, humanSize(sent), secs)
		}
		one, _, sent1, err1 := recvSpeed(host, f, dir, 1, 5*time.Second)
		msg := state + " " + line("Fast send, 1 connection", one, sent1, err1, 5)
		best := one
		if streams > 1 && manyStreamsOK(path.Clean("/"+dir)+"/") {
			many, used, sentN, errN := recvSpeed(host, f, dir, streams, 6*time.Second)
			msg += " " + line(fmt.Sprintf("Fast send, %d connections", used), many, sentN, errN, 6)
			if many > best {
				best = many
			}
		}
		ftpBps, ferr := f.speedTest(dir, 5*time.Second, 1<<30)
		if ferr != nil {
			msg += " Plain FTP: failed (" + ferr.Error() + ")."
		} else {
			msg += fmt.Sprintf(" Plain FTP: %.1f MB/s.", ftpBps/1e6)
		}
		res["mbs"], res["ftpMbs"] = best/1e6, ftpBps/1e6
		switch {
		case best == 0 && ftpBps > 0:
			msg += " Fast send isn't working on this PS5 yet. Turn Fast send off for now and sends will use plain FTP."
		case best > 0 && best < ftpBps*0.85:
			msg += " Plain FTP is faster here. Turn Fast send off for now."
		case best > 0:
			res["ok"] = true
		}
		res["msg"] = msg
		return res, nil
	})
	api("/api/ps5/speed", func(r *http.Request) (any, error) {
		host, port, dir := a.ps5()
		if host == "" {
			return nil, errors.New("Set your PS5's IP address first.")
		}
		a.mu.Lock()
		busy := a.build.PS5Busy
		a.mu.Unlock()
		if busy {
			return nil, errors.New("Wait for the current job to finish first, it would skew the result.")
		}
		res := map[string]any{}
		li, lerr := linkInfo(host)
		if lerr == nil {
			res["link"] = li
		}
		f, err := ftpDial(host, port)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		bps, err := f.speedTest(dir, 6*time.Second, 1<<30)
		if err != nil {
			return nil, err
		}
		res["mbs"] = bps / 1e6
		res["verdict"] = speedVerdict(li, lerr == nil, bps/1e6)
		return res, nil
	})
	api("/api/storeinfo/refresh", func(r *http.Request) (any, error) {
		var b struct{ Key string }
		body(r, &b)
		a.mu.Lock()
		e := a.lib.Games[b.Key]
		a.mu.Unlock()
		if e == nil {
			return nil, errors.New("That game isn't in the library.")
		}
		select {
		case a.artKick <- b.Key:
		default:
			return nil, errors.New("Busy fetching game info, try again in a moment.")
		}
		return "Looking up " + e.Name + " again…", nil
	})
	api("/api/payload/send", func(r *http.Request) (any, error) {
		var b struct{ Path string }
		body(r, &b)
		return a.runPayload(b.Path, 0)
	})
	api("/api/payload/add", func(r *http.Request) (any, error) {
		p, err := pickFile("Pick a payload to send to the PS5", "PS5 payloads", "*.elf;*.bin")
		if err != nil || p == "" {
			return nil, err
		}
		if _, err := readPayload(p); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.addPayloadLocked(p)
		a.mu.Unlock()
		return "Added " + filepath.Base(p) + ".", nil
	})
	api("/api/payload/remove", func(r *http.Request) (any, error) {
		var b struct{ Path string }
		body(r, &b)
		a.mu.Lock()
		defer a.mu.Unlock()
		var keep []PayloadItem
		for _, p := range a.cfg.Payloads {
			if !strings.EqualFold(p.Path, b.Path) {
				keep = append(keep, p)
			}
		}
		a.cfg.Payloads = keep
		saveJSON("settings.json", a.cfg)
		return nil, nil
	})
	api("/api/payload/get", func(r *http.Request) (any, error) {
		var b struct {
			Name string
			Send bool
		}
		body(r, &b)
		p, err := downloadPayload(b.Name)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.addPayloadLocked(p)
		a.mu.Unlock()
		if b.Send {
			return a.runPayload(p, 0)
		}
		return "Downloaded the latest " + b.Name + ".", nil
	})
	api("/api/pcdrive/setup", func(r *http.Request) (any, error) {
		var b struct {
			Root string
			Use  bool
		}
		body(r, &b)
		return a.setupPCDrive(b.Root, b.Use)
	})
	api("/api/ps5/move", func(r *http.Request) (any, error) {
		var b struct {
			Paths []string
			Drive string
		}
		body(r, &b)
		return nil, a.startPS5Move(b.Paths, b.Drive)
	})
	a.fxRoutes(api, func(r *http.Request, v any) { body(r, v) })
	api("/api/pkg/install", func(r *http.Request) (any, error) {
		var b struct{ Key string }
		body(r, &b)
		return nil, a.startPkgInstall(b.Key)
	})
	a.autoloadRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.homeRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.profRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.fwdRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.cheatRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.gameIDRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.cheatViewRoutes(api, func(r *http.Request, v any) { body(r, v) })
	a.emuRoutes(api, func(r *http.Request, v any) { body(r, v) })
	api("/api/saves/scan", func(r *http.Request) (any, error) {
		go a.refreshSaves()
		return nil, nil
	})
	api("/api/saves/backup", func(r *http.Request) (any, error) {
		var b struct{ Items []saveReq }
		body(r, &b)
		return nil, a.startSaveBackup(b.Items)
	})
	api("/api/saves/delete", func(r *http.Request) (any, error) {
		var b struct{ Items []saveReq }
		body(r, &b)
		return nil, a.startSaveDelete(b.Items)
	})
	api("/api/saves/restore", func(r *http.Request) (any, error) {
		var b struct{ ID string }
		body(r, &b)
		return nil, a.startSaveRestore(b.ID)
	})
	api("/api/saves/remove", func(r *http.Request) (any, error) {
		var b struct{ ID string }
		body(r, &b)
		dir, err := backupDir(b.ID)
		if err != nil {
			return nil, err
		}
		if _, err := recycle(dir, false); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.saves.Backups = loadBackups()
		a.mu.Unlock()
		return nil, nil
	})
	api("/api/ps5/delete", func(r *http.Request) (any, error) {
		var b struct{ Paths []string }
		body(r, &b)
		return nil, a.startPS5Delete(b.Paths)
	})
	api("/api/ps5/drives", func(r *http.Request) (any, error) {
		a.mu.Lock()
		if strings.TrimSpace(a.cfg.PS5IP) == "" {
			a.mu.Unlock()
			return nil, errors.New("Set your PS5's IP address first.")
		}
		a.ps5info.Busy = false // a manual refresh always runs
		a.mu.Unlock()
		a.refreshPS5()
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.ps5info, nil
	})
	api("/api/ps5/cleanup", func(r *http.Request) (any, error) {
		host, port, dir := a.ps5()
		if host == "" {
			return nil, errors.New("Set your PS5's IP address first.")
		}
		a.mu.Lock()
		busy := a.build.PS5Busy
		a.mu.Unlock()
		if busy {
			return nil, errors.New("Wait for the current job to finish first.")
		}
		f, err := ftpDial(host, port)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		gone, err := f.removePartials(dir)
		if gone == nil {
			gone = []string{}
		}
		return gone, err
	})
	api("/api/ps5/list", func(r *http.Request) (any, error) {
		var b struct {
			IP   string
			Port int
			Dir  string
		}
		body(r, &b)
		host, port, dir := a.ps5()
		if b.IP != "" {
			host = strings.TrimSpace(b.IP)
		}
		if b.Port > 0 {
			port = b.Port
		}
		if b.Dir != "" {
			dir = b.Dir
		}
		if host == "" {
			return nil, errors.New("Type your PS5's IP address first.")
		}
		f, err := ftpDial(host, port)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		ents, err := f.list(dir)
		if err != nil {
			return map[string]any{"dir": dir, "entries": []ftpEntry{}, "missing": true}, nil
		}
		if ents == nil {
			ents = []ftpEntry{}
		}
		return map[string]any{"dir": dir, "entries": ents}, nil
	})
	api("/api/build/cancel", func(r *http.Request) (any, error) {
		var b struct{ Lane string } // "build", "ps5", or "" for both
		body(r, &b)
		a.stopLane(b.Lane)
		return nil, nil
	})
	api("/api/alert/test", func(r *http.Request) (any, error) {
		a.mu.Lock()
		ntfy, discord := a.cfg.AlertNtfy, a.cfg.AlertDiscord
		a.mu.Unlock()
		if strings.TrimSpace(ntfy) == "" && strings.TrimSpace(discord) == "" {
			return nil, errors.New("Add an ntfy topic or a Discord webhook link first.")
		}
		if err := sendAlert(ntfy, discord, "Loadout: test alert", "If you can read this on your phone, alerts are working.", false); err != nil {
			return nil, errors.New("The alert didn't go through (" + err.Error() + ").")
		}
		return "Test alert sent. Check your phone.", nil
	})
	api("/api/build/clear", func(r *http.Request) (any, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if !a.build.Running {
			a.build = BuildState{}
		}
		return nil, nil
	})
	api("/api/open", func(r *http.Request) (any, error) {
		var b struct {
			Path   string
			Select bool
			Create bool // only for the image output folder
		}
		body(r, &b)
		if b.Path == "" {
			return nil, errors.New("No path.")
		}
		if b.Create {
			os.MkdirAll(b.Path, 0755)
		}
		return nil, openPath(b.Path, b.Select)
	})
	api("/api/pick", func(r *http.Request) (any, error) {
		var b struct{ Title, Start string }
		body(r, &b)
		return pickFolder(b.Title, b.Start)
	})
	api("/api/export", func(r *http.Request) (any, error) {
		p, err := pickSave("Save game list", "PS5 Game Library.csv")
		if err != nil || p == "" {
			return "", err
		}
		if !strings.HasSuffix(strings.ToLower(p), ".csv") {
			p += ".csv"
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		return p, exportCSV(a.lib, p)
	})
	api("/api/clear-missing", func(r *http.Request) (any, error) { return a.clearMissing(), nil })
	api("/api/copy", func(r *http.Request) (any, error) {
		var b struct{ Text string }
		body(r, &b)
		return nil, copyText(b.Text)
	})
	return mux
}

// serve starts the local server and returns the URL to load.
func (a *App) serve() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	host := ln.Addr().String()
	h := a.handler()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host { // blocks DNS-rebinding style access from web pages
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	return "http://" + host + "/#" + a.token, nil
}

// cleanTemp removes unfinished images left by a crash or forced quit.
func (a *App) cleanTemp() {
	out := a.outDir()
	if out == "" {
		return
	}
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".exfat.building") {
			os.Remove(filepath.Join(out, e.Name()))
		}
	}
}

// shutdown stops a running build and waits for it to clean up.
func (a *App) shutdown() {
	atomic.StoreInt32(&a.cancel, 1)
	a.stopLane("ps5")
	for i := 0; i < 50; i++ {
		a.mu.Lock()
		running := a.build.Running
		a.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.cleanTemp()
}

func safeOr(o NameOpts, s string) string {
	if o.Safe {
		return safeName(s)
	}
	return s
}

// ---------------- send to PS5 ----------------

type SentInfo struct {
	Dir  string    `json:"dir"`
	Host string    `json:"host"`
	At   time.Time `json:"at"`
	Size int64     `json:"size"`
}

func (a *App) ps5() (string, int, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port, a.cfg.PS5Dir
}

// sendImage uploads one image over FTP, reporting progress on job j.
func (a *App) sendImage(j *BuildJob, img string) (string, error) {
	host, port, dir := a.ps5()
	if host == "" {
		return "", errors.New("set your PS5's IP address in Settings first")
	}
	if j.Dest != "" {
		dir = j.Dest
	}
	defer func() { go a.refreshPS5() }()
	lane := j.Kind == "build" // built in this run: progress goes in the job's send fields
	a.mu.Lock()
	if lane {
		j.SendState, j.SendPct, j.SendNote = "sending", 0, "Connecting to the PS5"
	} else {
		j.State, j.Pct, j.Note = "sending", 0, "Connecting to the PS5"
	}
	a.build.Sending, a.build.SendFile = j.Name, filepath.Base(img)
	a.mu.Unlock()
	var f *ftpConn
	var err error
	var p5uWhy int32 // which side PS5Upload says is holding a send up
	a.mu.Lock()
	plain := a.cfg.PlainFTP
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.sendProg, j)
		a.sumSendLocked()
		a.mu.Unlock()
	}()
	start := time.Now()
	var last time.Time
	var startAt int64 = -1
	var spAt time.Time
	var spSent int64
	var spRate float64
	progress := func(sent, total int64) {
		if a.ps5Stopped(j) {
			return
		}
		if startAt < 0 {
			startAt = sent
		}
		if time.Since(last) < 150*time.Millisecond && sent < total {
			return
		}
		last = time.Now()
		// speed over the last few seconds, so a slow start doesn't skew it
		if now := time.Now(); spAt.IsZero() {
			spAt, spSent = now, sent
		} else if dt := now.Sub(spAt).Seconds(); dt >= 1 {
			inst := float64(sent-spSent) / dt
			if spRate == 0 {
				spRate = inst
			} else {
				spRate = 0.7*spRate + 0.3*inst
			}
			spAt, spSent = now, sent
		}
		speed := spRate
		a.mu.Lock()
		pct := 0
		if total > 0 {
			pct = int(sent * 1000 / total)
		}
		eta := int64(0)
		if speed > 0 {
			eta = int64(float64(total-sent) / speed)
		}
		if lane {
			j.SendNote, j.SendPct = "", pct
		} else {
			j.Note, j.Pct = "", pct
		}
		_ = eta
		a.sendProg[j] = &sendProg{left: total - sent, speed: speed}
		why := p5uWhy
		if speed <= 0 || speed >= 3<<20 {
			why = 0
		}
		if f != nil {
			why = atomic.LoadInt32(&f.slow)
		}
		switch why {
		case 1:
			a.build.SendSlow = "slow because this PC's drive is busy (reading the image is what's taking the time)"
		case 2:
			a.build.SendSlow = "slow because the PS5 is taking the data slowly (its drive or the network)"
		default:
			a.build.SendSlow = ""
		}
		a.sumSendLocked()
		a.mu.Unlock()
	}
	cancelled := func() bool { return a.ps5Stopped(j) }
	var resumed bool
	used := 0
	via, fellBack := "", ""
	if !plain && p5uAvailable() {
		note := func(s string) {
			a.mu.Lock()
			if lane {
				j.SendNote = s
			} else {
				j.Note = s
			}
			a.mu.Unlock()
		}
		note("Getting PS5Upload ready on the PS5")
		started, perr := p5uSend(host, img, path.Join(dir, filepath.Base(img)), progress, func(b string) {
			switch b {
			case "source":
				p5uWhy = 1
			case "network", "console", "disk", "sink":
				p5uWhy = 2
			default:
				p5uWhy = 0
			}
		}, cancelled)
		switch {
		case started && perr != nil:
			return "", perr
		case started:
			via = "PS5Upload"
		case errors.Is(perr, errFTPCancelled):
			return "", perr
		default:
			fellBack = perr.Error()
			note("Connecting to the PS5")
		}
	}
	if via == "" {
		f, err = ftpDial(host, port)
		if err != nil {
			return "", err
		}
		defer f.Close()
		a.mu.Lock()
		slow, streams := !a.cfg.FastSend, a.cfg.SendStreams
		a.mu.Unlock()
		if streams == 0 {
			streams = defaultStreams
		}
		// a cut-off fast send already holds its space on the PS5
		if !hasFastResume(host, dir, img) {
			if err := a.checkRoom(f, host, dir, img, j.DestLabel); err != nil {
				return "", err
			}
		}
		fast := !slow && a.ensureReceiver(host, func() bool { return a.ps5Stopped(j) })
		if a.ps5Stopped(j) {
			return "", errFTPCancelled
		}
		if fast {
			resumed, used, err = fastUpload(f, host, img, dir, streams, progress, cancelled)
		} else {
			resumed, err = f.upload(img, dir, progress, cancelled)
		}
	}
	if err != nil {
		return "", err
	}
	fi, _ := os.Stat(img)
	a.mu.Lock()
	a.sent[strings.ToLower(filepath.Base(img))] = SentInfo{Dir: dir, Host: host, At: time.Now(), Size: fi.Size()}
	saveJSON("sent.json", a.sent)
	a.mu.Unlock()
	how := ""
	if secs := time.Since(start).Seconds(); secs > 1 && startAt >= 0 && fi.Size() > startAt {
		how = ", " + humanSize(int64(float64(fi.Size()-startAt)/secs)) + "/s"
	}
	if via != "" {
		how += ", through " + via
	} else if fellBack != "" {
		how += ". PS5Upload couldn't be used (" + fellBack + "), so this went over FTP"
	}
	if used > 1 {
		how += fmt.Sprintf(", fast send over %d connections", used)
	} else if used == 1 {
		how += ", fast send"
	}
	switch {
	case startAt >= 0 && startAt == fi.Size():
		return "Already on the PS5", nil
	case resumed:
		return "Sent to the PS5 (picked up where it left off) in " + fmtDur(time.Since(start)) + how, nil
	}
	return "Sent to the PS5 in " + fmtDur(time.Since(start)) + how, nil
}

type sendItem struct{ Key, Name, Image, Drive string }

// startSend queues .exfat images for upload.
func (a *App) startSend(items []sendItem) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	var jobs []*BuildJob
	for _, it := range items {
		if !strings.HasSuffix(strings.ToLower(it.Image), ".exfat") || !fileExists(it.Image) {
			continue
		}
		fi, _ := os.Stat(it.Image)
		j := &BuildJob{Key: it.Key, Kind: "send", Name: it.Name, Out: filepath.Base(it.Image), State: "queued", Size: fi.Size(), image: it.Image}
		j.Dest, j.DestLabel = a.destLocked(it.Drive)
		jobs = append(jobs, j)
	}
	if len(jobs) == 0 {
		return errors.New("Those games don't have an image to send yet.")
	}
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	return nil
}

// LinkInfo describes the PC's network adapter that reaches the PS5.
type LinkInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"` // ethernet, wifi, other
	Mbps        int64  `json:"mbps"`
}

// speedVerdict explains a measured upload speed in plain words.
func speedVerdict(li LinkInfo, haveLink bool, mbs float64) string {
	pc := ""
	if haveLink {
		kind := map[string]string{"wifi": "Wi-Fi", "ethernet": "Ethernet"}[li.Kind]
		if kind == "" {
			kind = "a network adapter"
		}
		pc = fmt.Sprintf("Your PC reaches the PS5 over %s (%s), linked at %d Mbps. ", kind, li.Name, li.Mbps)
		if li.Mbps <= 0 {
			pc = fmt.Sprintf("Your PC reaches the PS5 over %s (%s). ", kind, li.Name)
		}
	}
	gameMin := 50e9 / (mbs * 1e6) / 60 // minutes for a 50 GB game
	tail := fmt.Sprintf(" At this speed a 50 GB game takes about %s.", fmtDur(time.Duration(gameMin*float64(time.Minute))))
	switch {
	case haveLink && li.Kind == "ethernet" && li.Mbps > 0 && li.Mbps <= 100:
		return pc + "That 100 Mbps link is the bottleneck: it can't go faster than about 12 MB/s. It's usually an old Cat5 cable or a 100 Mbps switch or router port. A Cat5e or Cat6 cable into a gigabit port fixes it." + tail
	case haveLink && li.Kind == "wifi":
		return pc + "Wi-Fi is the likely bottleneck. Plugging the PC (and the PS5) into the router with cables is the biggest speed-up." + tail
	case mbs < 14:
		return pc + "Your PC's side looks fine, so the slow link is on the PS5's side: it's probably on Wi-Fi, or its cable or switch port only runs at 100 Mbps. Check Settings › Network › Connection Status on the PS5." + tail
	case mbs < 40:
		return pc + "That's decent but below what gigabit can do. If the PS5 is on Wi-Fi, a cable will help. Some PS5 FTP servers also top out around here." + tail
	}
	return pc + "That's a healthy gigabit speed, the network isn't holding you back." + tail
}

var (
	awakeCacheMu    sync.Mutex
	awakeCacheAt    time.Time
	awakeCacheNotes []string
)

// cachedAwakeNotes asks Windows about lid and standby at most every 30 s.
func cachedAwakeNotes() (string, []string) {
	awakeCacheMu.Lock()
	defer awakeCacheMu.Unlock()
	state, notes := "", awakeCacheNotes
	if time.Since(awakeCacheAt) > 30*time.Second {
		state, notes = awakeNotes()
		awakeCacheNotes, awakeCacheAt = notes, time.Now()
	} else {
		state, _ = awakeStateOnly()
	}
	if notes == nil {
		notes = []string{}
	}
	return state, notes
}
