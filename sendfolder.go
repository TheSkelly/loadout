package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Sending a game as its folder, with no exFAT image. ShadowMountPlus runs
// folder games from the same places it finds images (etaHEN/games and
// homebrew on each drive), as long as sce_sys/param.json is at the top of
// the folder, so the game's root goes there as it is. PS5Upload sends a
// folder of many files much faster than FTP and is used when it's there;
// FTP otherwise. A folder game is also one PS5Upload can patch on the
// console (its images are read-only there).

// startSendFolder queues games' folders for the PS5.
func (a *App) startSendFolder(items []sendItem) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	var jobs []*BuildJob
	for _, it := range items {
		g := a.gameByKey(it.Key)
		if g == nil || g.IsFile || g.IsDLC || g.Meta.Root == "" {
			continue
		}
		var size int64
		if e := a.lib.Games[libKey(g)]; e != nil {
			size = e.SizeBytes
		}
		name := g.Name
		if name == "" {
			name = g.Current
		}
		j := &BuildJob{Key: it.Key, Kind: "sendfolder", Name: name, Out: folderNameOnPS5(g), State: "queued", Size: size, image: g.Meta.Root}
		j.Dest, j.DestLabel = a.destLocked(it.Drive)
		jobs = append(jobs, j)
	}
	if len(jobs) == 0 {
		return errors.New("Those games have no folder with game files in it to send.")
	}
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	return nil
}

// folderNameOnPS5 is the game folder's name on the PS5: its name here, with
// its title ID in it so Loadout knows the game when it looks on the PS5.
func folderNameOnPS5(g *Game) string {
	n := strings.TrimSpace(filepath.Base(g.Path))
	if g.TitleID != "" && !strings.Contains(strings.ToUpper(n), strings.ToUpper(g.TitleID)) {
		n = strings.TrimSpace(n + " [" + g.TitleID + "]")
	}
	if n == "" || n == "." {
		n = g.TitleID
	}
	return n
}

// sendFolder copies a game's folder to the PS5's games folder on a drive.
func (a *App) sendFolder(j *BuildJob) (string, error) {
	host, port, _ := a.ps5()
	if host == "" {
		return "", errors.New("set your PS5's IP address in Settings first")
	}
	src := j.image
	if st, err := os.Stat(filepath.Join(src, "sce_sys", "param.json")); err != nil || st.IsDir() {
		return "", errors.New("the game folder has no sce_sys/param.json at its top, so the PS5 wouldn't see it as a game")
	}
	dest := path.Join(j.Dest, j.Out)
	total := folderSize(src)
	if free, slack := freeOn(host, j.Dest); free >= 0 && total > free+slack {
		return "", fmt.Errorf("not enough room on %s: %s free, this needs %s", j.DestLabel, humanSize(free), humanSize(total))
	}
	start := time.Now()
	a.mu.Lock()
	j.State, j.Pct, j.Note, j.Size = "sending", 0, "Getting ready", total
	a.build.Sending, a.build.SendFile = j.Name, j.Out
	a.mu.Unlock()
	prog := func(done, all int64) {
		if all <= 0 {
			all = total
		}
		a.mu.Lock()
		j.Note = ""
		if all > 0 {
			j.Pct = int(done * 1000 / all)
		}
		if secs := time.Since(start).Seconds(); secs > 0 {
			speed := float64(done) / secs
			a.build.SendSpeed = int64(speed)
			if speed > 0 {
				a.build.SendETA = int64(float64(all-done) / speed)
			}
		}
		a.mu.Unlock()
	}
	cancelled := func() bool { return a.ps5Stopped(j) }
	var err error
	if p5uAvailable() {
		err = p5uSendDir(host, src, dest, prog, cancelled)
	} else {
		var f *ftpConn
		if f, err = ftpDial(host, port); err == nil {
			if err = f.mkdirAll(dest); err == nil {
				_, err = uploadTreeFiles(f, src, dest, prog, cancelled)
			}
			f.Close()
		}
	}
	go a.refreshPS5()
	if err != nil {
		if errors.Is(err, errFTPCancelled) {
			return "Stopped partway. Send the folder again to finish it", err
		}
		return "", err
	}
	return fmt.Sprintf("Sent the game folder to %s in %s. ShadowMountPlus adds it to the home screen", j.DestLabel, fmtDur(time.Since(start))), nil
}

// p5uSendDir sends a folder's contents into dest on the PS5 with PS5Upload.
func p5uSendDir(host, src, dest string, prog func(done, total int64), cancelled func() bool) error {
	base, err := p5uReady(host, cancelled)
	if err != nil {
		return err
	}
	var open struct {
		JobID string `json:"job_id"`
	}
	if err := p5uCall("POST", base+"/api/transfer/dir", map[string]string{"addr": host, "src_dir": src, "dest_root": dest}, &open); err != nil {
		return err
	}
	if open.JobID == "" {
		return errors.New("the PS5Upload engine didn't start the transfer")
	}
	misses := 0
	for {
		time.Sleep(500 * time.Millisecond)
		if cancelled() {
			p5uCall("POST", base+"/api/jobs/"+open.JobID+"/cancel", map[string]string{}, nil)
			return errFTPCancelled
		}
		var st struct {
			Status     string `json:"status"`
			BytesSent  int64  `json:"bytes_sent"`
			TotalBytes int64  `json:"total_bytes"`
			Error      string `json:"error"`
		}
		if err := p5uCall("GET", base+"/api/jobs/"+open.JobID, nil, &st); err != nil {
			if misses++; misses > 20 {
				return fmt.Errorf("lost contact with the PS5Upload engine (%v)", err)
			}
			continue
		}
		misses = 0
		switch st.Status {
		case "done", "completed", "complete":
			return nil
		case "failed", "error", "cancelled":
			if st.Error == "" {
				st.Error = "the transfer " + st.Status
			}
			return errors.New(st.Error)
		}
		prog(st.BytesSent, st.TotalBytes)
	}
}
