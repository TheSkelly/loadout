//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Development build for testing the UI and logic outside Windows.

func diskFree(path string) int64 {
	var st syscall.Statfs_t
	for path != "" {
		if syscall.Statfs(path, &st) == nil {
			return int64(st.Bavail) * int64(st.Bsize)
		}
		parent := parentDir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return -1
}

func openPath(p string, sel bool) error { fmt.Println("open:", p, sel); return nil }
func pickFolder(title, start string) (string, error) {
	return os.Getenv("TT_PICK"), nil
}
func pickSave(title, name string) (string, error) { return "", errors.New("not available") }

// test stand-in for the Recycle Bin
func recycle(path string, noAsk bool) (bool, error) {
	trash := os.Getenv("TT_TRASH")
	if trash == "" {
		return true, errors.New("no recycle bin here")
	}
	os.MkdirAll(trash, 0755)
	return false, os.Rename(path, trash+"/"+filepathBase(path))
}

func copyText(t string) error { fmt.Println("copy:", t); return nil }

func main() {
	a := newApp()
	url, err := a.serve()
	if err != nil {
		panic(err)
	}
	if a.cfg.Folder != "" {
		a.startScan("")
	}
	fmt.Println(url)
	if f := os.Getenv("TT_URLFILE"); f != "" {
		os.WriteFile(f, []byte(url), 0644)
	}
	select {}
}

func linkInfo(ip string) (LinkInfo, error) {
	if v := os.Getenv("TT_LINK"); v != "" { // test stand-in: "ethernet:100"
		var li LinkInfo
		parts := strings.SplitN(v, ":", 2)
		li.Kind, li.Name = parts[0], "Test adapter"
		fmt.Sscanf(parts[1], "%d", &li.Mbps)
		return li, nil
	}
	return LinkInfo{}, errors.New("not available here")
}

// listVolumes fakes drives for testing: TT_DRIVES="G|/tmp/g|exFAT|PS5_JB;J|/tmp/j|NTFS|Data"
func listVolumes() []volInfo {
	var res []volInfo
	for _, part := range strings.Split(os.Getenv("TT_DRIVES"), ";") {
		f := strings.Split(part, "|")
		if len(f) < 4 {
			continue
		}
		var st syscall.Statfs_t
		v := volInfo{Root: f[1], Letter: strings.TrimRight(f[0], `\\/:`), FS: f[2], Label: f[3], Total: -1, Free: -1}
		if syscall.Statfs(f[1], &st) == nil {
			v.Total, v.Free = int64(st.Blocks)*int64(st.Bsize), int64(st.Bavail)*int64(st.Bsize)
		}
		res = append(res, v)
	}
	return res
}

func pickFile(title, filterName, filterSpec string) (string, error) {
	return os.Getenv("TT_PICKFILE"), nil
}

// tray and keep-awake only exist on Windows
const hasTray = false

func setCloseToTray(on bool)         {}
func keepAwakeLoop(busy func() bool) {}

func hideChild(c *exec.Cmd) {}

func awakeNotes() (string, []string) { return "idle", nil }

func awakeStateOnly() (string, bool) { return "idle", true }
