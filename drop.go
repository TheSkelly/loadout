package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Files dragged from Explorer and dropped on an emulator's page. The window
// can't learn where a dropped file lives on the PC, only read it, so the file
// is streamed from the window to Loadout and from there straight on to the
// PS5, with no extra copy on this PC. (A ZIP that has to be opened first is
// the exception: it is saved to a temporary file, then handled as usual.)

func (a *App) dropRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/emu/drop", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != a.token || r.Method != http.MethodPost {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(msg string, err error) {
			if err != nil {
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]string{"msg": msg}})
		}
		q := r.URL.Query()
		dir, ok := a.fxAllowed(q.Get("dir"))
		name, nerr := fxName(q.Get("name"))
		size, _ := strconv.ParseInt(q.Get("size"), 10, 64)
		if !ok || nerr != nil || size <= 0 {
			io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
			reply("", errors.New("That file can't be added here."))
			return
		}
		if q.Get("unzip") == "1" && strings.EqualFold(path.Ext(name), ".zip") {
			reply(a.dropZip(r.Body, dir, name, size))
			return
		}
		reply(a.dropFile(r.Body, dir, name, size))
	})
}

// dropZip keeps a dropped ZIP in a temporary file and queues the usual unzip-and-send job.
func (a *App) dropZip(body io.Reader, dir, name string, size int64) (string, error) {
	if olds, _ := filepath.Glob(filepath.Join(os.TempDir(), "loadout-drop-*")); len(olds) > 0 {
		for _, o := range olds {
			if fi, err := os.Stat(o); err == nil && time.Since(fi.ModTime()) > 12*time.Hour {
				os.RemoveAll(o)
			}
		}
	}
	tmpDir, err := os.MkdirTemp("", "loadout-drop-")
	if err != nil {
		return "", err
	}
	local := filepath.Join(tmpDir, name)
	out, err := os.Create(local)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, body)
	out.Close()
	if err != nil || n != size {
		os.RemoveAll(tmpDir)
		return "", errors.New("The ZIP didn't arrive whole, so it wasn't used. There may not be enough room in this PC's temporary folder.")
	}
	a.mu.Lock()
	j := &BuildJob{Key: "fxdir:" + dir, Kind: "fxzipflat", Name: name, Out: name, State: "queued", Size: size, image: local, Dest: dir}
	a.addJobsLocked([]*BuildJob{j})
	a.enqueuePS5Locked(j)
	a.mu.Unlock()
	return "queued", nil
}

// dropFile streams one dropped file to the PS5 and shows it in the job list.
func (a *App) dropFile(body io.Reader, dir, name string, size int64) (string, error) {
	host, port, _ := a.ps5()
	if host == "" {
		return "", errors.New("Set your PS5's IP address in Settings first.")
	}
	a.mu.Lock()
	j := &BuildJob{Key: "fx:" + path.Join(dir, name), Kind: "fxup", Name: name + " to the PS5", Out: name, State: "copying", Note: "Connecting to the PS5", Size: size, Dest: dir}
	a.addJobsLocked([]*BuildJob{j})
	if !a.build.PS5Busy {
		a.build.PS5Busy, a.build.Running = true, true
		a.ps5st = ps5Stats{start: time.Now()}
	}
	j.gen = atomic.LoadInt32(&a.ps5stop)
	a.dropActive++
	a.mu.Unlock()

	start := time.Now()
	var last time.Time
	err := func() error {
		f, err := ftpDial(host, port)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.uploadStream(body, size, dir, name, func(sent, total int64) {
			if time.Since(last) < 200*time.Millisecond && sent < total {
				return
			}
			last = time.Now()
			a.mu.Lock()
			j.Note = ""
			if total > 0 {
				j.Pct = int(sent * 1000 / total)
			}
			a.mu.Unlock()
		}, func() bool { return a.ps5Stopped(j) })
	}()

	a.mu.Lock()
	defer a.mu.Unlock()
	msg := ""
	switch {
	case errors.Is(err, errFTPCancelled):
		j.State, j.Note = "cancelled", "Stopped"
		a.ps5st.stopped = true
	case err != nil:
		j.State, j.Note = "failed", err.Error()
		a.ps5st.failed++
	default:
		j.State, j.Pct, j.Note = "done", 1000, "Copied to the PS5 in "+fmtDur(time.Since(start))
		a.ps5st.copied++
		msg = j.Note
	}
	a.dropActive--
	if a.dropActive == 0 && len(a.ps5q) == 0 && a.ps5active == 0 {
		a.build.PS5Busy = false
		note := ""
		if a.ps5st.copied > 0 {
			note = fmt.Sprintf("Copied %d file(s).", a.ps5st.copied)
		}
		if a.ps5st.failed > 0 {
			note = strings.TrimSpace(note + fmt.Sprintf(" %d PS5 job(s) failed.", a.ps5st.failed))
		}
		a.finishLaneLocked(note)
		go a.refreshPS5()
	}
	return msg, err
}
