package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PayloadItem is a payload file the user keeps in Loadout.
type PayloadItem struct {
	Path string
	Port int `json:",omitempty"` // 0 = use the loader port setting
}

// PayloadView is a payload as the UI sees it.
type PayloadView struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Port    int    `json:"port"`
	Missing bool   `json:"missing"`
	Known   string `json:"known"` // "shsrv", "ftpsrv" for the ones Loadout can download
}

// knownPayloads are open-source payloads Loadout can fetch for you.
var knownPayloads = map[string]string{
	"shsrv":  "https://github.com/ps5-payload-dev/shsrv/releases/latest/download/shsrv-ps5.elf",
	"ftpsrv": "https://github.com/ps5-payload-dev/ftpsrv/releases/latest/download/ftpsrv-ps5.elf",
}

const defaultLoaderPort = 9021

func knownName(p string) string {
	b := strings.ToLower(filepath.Base(p))
	for k := range knownPayloads {
		if strings.HasPrefix(b, k+"-ps5") || strings.HasPrefix(b, k+".") {
			return k
		}
	}
	return ""
}

func (a *App) loaderPortLocked() int {
	if a.cfg.PayloadPort > 0 {
		return a.cfg.PayloadPort
	}
	return defaultLoaderPort
}

func (a *App) payloadViewsLocked() []PayloadView {
	res := []PayloadView{}
	for _, p := range a.cfg.Payloads {
		v := PayloadView{Path: p.Path, Name: filepath.Base(p.Path), Port: p.Port, Known: knownName(p.Path), Size: -1}
		if fi, err := os.Stat(p.Path); err == nil {
			v.Size = fi.Size()
		} else {
			v.Missing = true
		}
		res = append(res, v)
	}
	return res
}

// readPayload loads a payload and checks it looks like one.
func readPayload(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("That payload file isn't there any more.")
	}
	if fi.Size() == 0 || fi.Size() > 64<<20 {
		return nil, errors.New("That file doesn't look like a payload (it's empty or very large).")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".elf" && !bytes.HasPrefix(data, []byte("\x7fELF")) {
		return nil, errors.New("That .elf file isn't a valid ELF payload.")
	}
	return data, nil
}

// sendPayload streams a payload to the PS5's ELF loader, like "nc ps5 9021 < file".
func sendPayload(host string, port int, data []byte) error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 4*time.Second)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return fmt.Errorf("the PS5 didn't answer on port %d. Is it on and on the same network?", port)
		}
		return fmt.Errorf("nothing is listening on port %d. Jailbreak the PS5 first so its ELF loader is running", port)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := c.Write(data); err != nil {
		return fmt.Errorf("the PS5 dropped the connection while the payload was sending: %s", shortNetErr(err))
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	// give the loader a moment to read everything before we hang up
	c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	io.Copy(io.Discard, c)
	return nil
}

// downloadPayload fetches a known payload into Loadout's own folder.
func downloadPayload(name string) (string, error) {
	url, ok := knownPayloads[name]
	if !ok {
		return "", errors.New("unknown payload")
	}
	cl := &http.Client{Timeout: 90 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return "", fmt.Errorf("Couldn't download %s. Check your internet connection. (%v)", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Couldn't download %s (GitHub said %s).", name, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", fmt.Errorf("The %s download was cut off. Try again.", name)
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		return "", fmt.Errorf("The %s download wasn't a valid payload.", name)
	}
	dir := filepath.Join(appDir(), "payloads")
	os.MkdirAll(dir, 0755)
	dst := filepath.Join(dir, name+"-ps5.elf")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// addPayloadLocked remembers a payload file (no duplicates).
func (a *App) addPayloadLocked(path string) {
	for _, p := range a.cfg.Payloads {
		if strings.EqualFold(p.Path, path) {
			return
		}
	}
	a.cfg.Payloads = append(a.cfg.Payloads, PayloadItem{Path: path})
	saveJSON("settings.json", a.cfg)
}

// shellPayloadLocked is the shsrv file to auto-load, if there is one.
func (a *App) shellPayloadLocked() string {
	for _, p := range a.cfg.Payloads {
		if knownName(p.Path) == "shsrv" && fileExists(p.Path) {
			return p.Path
		}
	}
	return ""
}

// runPayload sends one payload and refreshes the PS5 view afterwards.
func (a *App) runPayload(path string, port int) (string, error) {
	a.mu.Lock()
	host := strings.TrimSpace(a.cfg.PS5IP)
	if port <= 0 {
		port = a.loaderPortLocked()
		for _, p := range a.cfg.Payloads {
			if strings.EqualFold(p.Path, path) && p.Port > 0 {
				port = p.Port
			}
		}
	}
	a.mu.Unlock()
	if host == "" {
		return "", errors.New("Set your PS5's IP address first.")
	}
	data, err := readPayload(path)
	if err != nil {
		return "", err
	}
	if err := sendPayload(host, port, data); err != nil {
		return "", errors.New(strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ".")
	}
	go func() {
		// servers like shsrv and ftpsrv take a moment to start listening
		time.Sleep(3 * time.Second)
		a.refreshSpace()
		a.mu.Lock()
		a.ps5info.Busy = false
		a.mu.Unlock()
		a.refreshPS5()
	}()
	return fmt.Sprintf("Sent %s to the PS5 (%s).", filepath.Base(path), humanSize(int64(len(data)))), nil
}

// maybeAutoShell loads shsrv when the PS5 is up but free space is unknown.
func (a *App) maybeAutoShell(info PS5Info) {
	if !info.Online || info.Shell {
		return
	}
	a.mu.Lock()
	p := a.shellPayloadLocked()
	ok := a.cfg.AutoShell && p != "" && time.Since(a.autoShellAt) > 10*time.Minute
	if ok {
		a.autoShellAt = time.Now()
	}
	a.mu.Unlock()
	if ok {
		if _, err := a.runPayload(p, 0); err == nil {
			a.mu.Lock()
			a.toast = append(a.toast, "Loaded shsrv on the PS5 so Loadout can show free space.")
			a.mu.Unlock()
		}
	}
}
