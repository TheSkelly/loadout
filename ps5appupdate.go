package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Keeping the PS5 app working without reinstalls.
//
// ShadowMountPlus mounts /data/homebrew/PPSA99880 as the app. When anything
// in that folder changes while the app is open or suspended, it can't
// remount it ("Device busy"), drops the app's mount link, and the PS5 then
// says the app can't be started until it's closed and remounted. So:
//
//   - A new build goes up to a holding folder ShadowMountPlus doesn't watch,
//     any time. The app folder is only swapped for it when the app asks:
//     it closes itself, and once it has stopped calling, Loadout renames the
//     folders. ShadowMountPlus mounts the new one within half a minute.
//   - The PC's addresses and the pairing token are also kept in the app's
//     data folder, which isn't part of the mount. Loadout rewrites them there
//     when this PC's address changes, so a new IP doesn't strand the app.

const (
	ps5AppNext = "/data/loadout/app-next/" + ps5AppID // a new build waits here
	ps5AppOld  = "/data/loadout/app-old"              // the build it replaced, deleted after
	ps5AppData = "/data/ps5-react/" + ps5AppID        // the app's FileSystem.dataDir
)

// isPS5AppJob: the app's first install or an update being staged.
func isPS5AppJob(j *BuildJob) bool {
	return j.Kind == "fxupdir" && (j.Key == "fx:"+path.Join(fwdRoot, ps5AppID) || j.Key == "fx:"+ps5AppNext)
}

// ps5AppURLs are the addresses the PS5 app tries, the one this PC reaches
// the PS5 from first: a VPN or a virtual adapter (NordVPN, WSL, Hyper-V)
// sorts first otherwise, and the PS5 can't reach those.
func (a *App) ps5AppURLs() []string {
	a.mu.Lock()
	ps5ip, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.RemotePort
	a.mu.Unlock()
	if port == 0 {
		port = defaultRemotePort
	}
	ips, ts := localAddrs()
	var urls []string
	if route := localIPToward(ps5ip); route != "" {
		urls = append(urls, fmt.Sprintf("http://%s:%d", route, port))
	}
	for _, ip := range ips {
		if u := fmt.Sprintf("http://%s:%d", ip, port); ip != ts && (len(urls) == 0 || urls[0] != u) {
			urls = append(urls, u)
		}
	}
	if ts != "" {
		urls = append(urls, fmt.Sprintf("http://%s:%d", ts, port))
	}
	return urls
}

type ps5AppConfig struct {
	PC    string   `json:"pc"`
	URLs  []string `json:"urls"`
	Token string   `json:"token"`
}

// syncPS5AppConfig keeps the app's data-folder copy of loadout.json current:
// this PC's addresses as they are now, with the token in the app folder's
// copy (an install writes that one, with a fresh pairing).
func syncPS5AppConfig(f *ftpConn, urls []string) {
	var paired, have ps5AppConfig
	b, err := f.get(path.Join(fwdRoot, ps5AppID, "loadout.json"), 8192)
	if err != nil || json.Unmarshal(b, &paired) != nil || paired.Token == "" || len(urls) == 0 {
		return
	}
	if b, err := f.get(path.Join(ps5AppData, "loadout.json"), 8192); err == nil {
		json.Unmarshal(b, &have)
	}
	if have.Token == paired.Token && strings.Join(have.URLs, " ") == strings.Join(urls, " ") {
		return
	}
	host, _ := os.Hostname()
	out, _ := json.MarshalIndent(ps5AppConfig{PC: host, URLs: urls, Token: paired.Token}, "", "  ")
	if f.mkdirAll(ps5AppData) == nil {
		f.put(path.Join(ps5AppData, "loadout.json"), out)
	}
}

// updatePS5App gets this build of the PS5 app ready on the PS5 when it has
// an older one. now is the app asking (it goes even with auto-update off).
// It answers "current", "staged" (ready to swap in), "queued" (on its way)
// or "no".
func (a *App) updatePS5App(now bool) string {
	src, stamp := ps5AppSource(), ps5AppStamp()
	if src == "" || stamp == "" {
		return "no"
	}
	dir := path.Join(fwdRoot, ps5AppID)
	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	for _, j := range a.build.Jobs {
		if isPS5AppJob(j) && (j.State == "queued" || j.State == "copying") {
			a.mu.Unlock()
			return "queued" // already on its way
		}
	}
	skip := host == "" || (!now && (!boolOr(a.cfg.PS5AppAuto, true) || !a.ps5info.Online))
	a.mu.Unlock()
	if skip {
		return "no"
	}
	f, err := ftpDial(host, port)
	if err != nil {
		return "no"
	}
	defer f.Close()
	if n, ok := f.size(path.Join(dir, "loadout.json")); !ok || n == 0 {
		return "no" // not installed: the first install is done from Settings, where it gets paired
	}
	syncPS5AppConfig(f, a.ps5AppURLs())
	if b, err := f.get(path.Join(dir, ps5AppStampFile), 64); err == nil && strings.TrimSpace(string(b)) == stamp {
		return "current"
	}
	// the stamp's name sorts last, so it's there only once the rest is
	if b, err := f.get(path.Join(ps5AppNext, ps5AppStampFile), 64); err == nil && strings.TrimSpace(string(b)) == stamp {
		return "staged"
	}
	stage := filepath.Join(os.TempDir(), "loadout-ps5app-update", ps5AppID)
	os.RemoveAll(filepath.Dir(stage))
	if err := copyTree(src, stage); err != nil {
		return "no"
	}
	os.Remove(filepath.Join(stage, "loadout.json"))
	if os.WriteFile(filepath.Join(stage, ps5AppStampFile), []byte(stamp), 0644) != nil {
		return "no"
	}
	// an older staged build goes first, so none of its files linger
	if _, err := f.list(ps5AppNext); err == nil {
		f.removeAll(ps5AppNext, true, func() {}, func() bool { return false })
	}
	a.mu.Lock()
	j := &BuildJob{Key: "fx:" + ps5AppNext, Kind: "fxupdir", Name: "Loadout app update for the PS5", Out: ps5AppID, State: "queued",
		Size: folderSize(stage), image: stage, Dest: ps5AppNext}
	a.addJobsLocked([]*BuildJob{j})
	a.enqueuePS5Locked(j)
	a.mu.Unlock()
	return "queued"
}

// swapPS5AppSoon swaps the staged build in once the app has closed: it
// stops calling Loadout when it does.
func (a *App) swapPS5AppSoon() error {
	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return errors.New("Loadout can't reach the PS5's FTP server.")
	}
	b, err := f.get(path.Join(ps5AppNext, ps5AppStampFile), 64)
	f.Close()
	if err != nil || strings.TrimSpace(string(b)) != ps5AppStamp() {
		return errors.New("The update isn't on the PS5 yet.")
	}
	go func() {
		for start := time.Now(); time.Since(start) < 90*time.Second; {
			time.Sleep(time.Second)
			a.mu.Lock()
			quiet := time.Since(a.storeHitAt) > 4*time.Second
			a.mu.Unlock()
			if quiet {
				break
			}
		}
		time.Sleep(2 * time.Second) // the app's process winds down
		a.swapPS5App(host, port)
	}()
	return nil
}

// swapPS5App puts the staged build where ShadowMountPlus looks, keeping the
// pairing, and clears away the old one.
func (a *App) swapPS5App(host string, port int) error {
	f, err := ftpDial(host, port)
	if err != nil {
		return err
	}
	defer f.Close()
	dir := path.Join(fwdRoot, ps5AppID)
	cfg, err := f.get(path.Join(dir, "loadout.json"), 8192)
	if err != nil || len(cfg) == 0 {
		return errors.New("the app on the PS5 has no pairing to keep")
	}
	if err := f.put(path.Join(ps5AppNext, "loadout.json"), cfg); err != nil {
		return err
	}
	if _, err := f.list(ps5AppOld); err == nil {
		f.removeAll(ps5AppOld, true, func() {}, func() bool { return false })
	}
	if err := f.rename(dir, ps5AppOld); err != nil {
		return err
	}
	if err := f.rename(ps5AppNext, dir); err != nil {
		f.rename(ps5AppOld, dir) // put the old one back
		return err
	}
	f.removeAll(ps5AppOld, true, func() {}, func() bool { return false })
	return nil
}
