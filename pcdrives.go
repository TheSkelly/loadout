package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// volInfo is a drive on the PC, as the platform layer reports it.
type volInfo struct {
	Root      string // "G:\"
	Letter    string // set by the test fake only
	Label, FS string
	Total     int64
	Free      int64
	System    bool // the Windows drive
	Removable bool
}

// PCDrive is a drive on the PC that can carry games to the PS5.
type PCDrive struct {
	Root    string `json:"root"`
	Letter  string `json:"letter"`
	Label   string `json:"label"`
	FS      string `json:"fs"`
	Total   int64  `json:"total"`
	Free    int64  `json:"free"`
	OK      bool   `json:"ok"`      // exFAT, so the PS5 can read it
	Ready   bool   `json:"ready"`   // the games folder exists
	Folder  string `json:"folder"`  // <drive>\etaHEN\games
	Images  int    `json:"images"`  // .exfat files already in it
	InUse   bool   `json:"inUse"`   // it's the current image output folder
	Problem string `json:"problem"` // why it can't be used as is
}

// ps5GamesSub is where ShadowMount+ looks on a USB drive (it also scans
// the drive's root and \homebrew).
var ps5GamesSub = filepath.Join("etaHEN", "games")

func scanPCDrives(outDir string) []PCDrive {
	out := strings.ToLower(filepath.Clean(outDir))
	var res []PCDrive
	for _, v := range listVolumes() {
		if v.System {
			continue
		}
		fs := strings.ToUpper(v.FS)
		if v.Letter == "" {
			v.Letter = strings.TrimRight(v.Root, `\/:`)
		}
		d := PCDrive{Root: v.Root, Letter: v.Letter, Label: v.Label, FS: v.FS, Total: v.Total, Free: v.Free,
			Folder: filepath.Join(v.Root, ps5GamesSub)}
		switch {
		case fs == "EXFAT":
			d.OK = true
		case fs == "FAT32":
			d.Problem = "FAT32 can't hold files over 4 GB, so most games won't fit. Reformat it as exFAT."
		default:
			continue // NTFS and friends: the PS5 can't read them, so don't list your PC's own drives
		}
		if st, err := os.Stat(d.Folder); err == nil && st.IsDir() {
			d.Ready = true
			if ents, err := os.ReadDir(d.Folder); err == nil {
				for _, e := range ents {
					if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".exfat") {
						d.Images++
					}
				}
			}
		}
		d.InUse = out != "." && out == strings.ToLower(filepath.Clean(d.Folder))
		res = append(res, d)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Root < res[j].Root })
	return res
}

// setupPCDrive creates the games folder on a drive and, if asked, saves images there.
func (a *App) setupPCDrive(root string, use bool) (string, error) {
	var d *PCDrive
	a.mu.Lock()
	out := a.outDir()
	a.mu.Unlock()
	for _, x := range scanPCDrives(out) {
		if strings.EqualFold(x.Root, root) {
			x := x
			d = &x
		}
	}
	if d == nil {
		return "", errors.New("That drive isn't connected any more.")
	}
	if !d.OK {
		return "", errors.New(d.Problem)
	}
	if err := os.MkdirAll(d.Folder, 0755); err != nil {
		return "", errors.New("Couldn't create the folder: " + err.Error())
	}
	name := d.Letter + ":"
	if d.Label != "" {
		name = d.Label + " (" + d.Letter + ":)"
	}
	if use {
		a.mu.Lock()
		running := a.build.Building
		if !running {
			a.rememberImageDirLocked(a.outDir())
			a.cfg.ImageOut = d.Folder
			saveJSON("settings.json", a.cfg)
		}
		a.mu.Unlock()
		if running {
			return "", errors.New("Wait for the current build to finish before switching drives.")
		}
		a.refreshPCDrives()
		return name + " is ready. New images will be built straight onto it.", nil
	}
	a.refreshPCDrives()
	return name + " is ready for the PS5.", nil
}

func (a *App) refreshPCDrives() {
	a.mu.Lock()
	out := a.outDir()
	a.mu.Unlock()
	d := scanPCDrives(out)
	a.mu.Lock()
	a.pcdrv = d
	a.mu.Unlock()
}

func (a *App) pcDriveLoop() {
	for {
		a.refreshPCDrives()
		time.Sleep(8 * time.Second)
	}
}
