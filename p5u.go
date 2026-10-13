package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Sending through PS5Upload (github.com/phantomptr/ps5upload, GPL-3.0).
// Loadout ships PS5Upload's own engine program unchanged and drives it the way
// PS5Upload's desktop app does: over a local web API. The engine loads its
// helper on the PS5 (through the ELF loader on port 9021) and sends files to
// it on port 9120, checking every file as it lands.

var (
	p5uMu    sync.Mutex
	p5uBase  string // http://127.0.0.1:port once the engine is up
	p5uProc  *exec.Cmd
	p5uStdin io.WriteCloser // held open: the engine exits when Loadout does
	p5uHelp  sync.Mutex     // one goroutine at a time loads the helper
	p5uHTTP  = &http.Client{Timeout: 40 * time.Second}
)

type p5uFail struct {
	at  time.Time
	err error
}

var p5uBad = map[string]p5uFail{} // guarded by p5uHelp

// p5uEnginePath finds the bundled engine next to Loadout.
func p5uEnginePath() string {
	if p := os.Getenv("TT_P5UENGINE"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "ps5upload-engine"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if fileExists(p) {
		return p
	}
	return ""
}

func p5uAvailable() bool { return p5uEnginePath() != "" }

// p5uStart makes sure the engine is running and returns its address.
func p5uStart() (string, error) {
	p5uMu.Lock()
	defer p5uMu.Unlock()
	if p5uBase != "" && p5uPing(p5uBase) {
		return p5uBase, nil
	}
	if p5uProc != nil && p5uProc.Process != nil {
		p5uProc.Process.Kill()
	}
	p5uBase = ""
	eng := p5uEnginePath()
	if eng == "" {
		return "", errors.New("the PS5Upload engine isn't installed with this copy of Loadout")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(eng)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PS5UPLOAD_ENGINE_PORT=%d", port), "PS5UPLOAD_PARENT_WATCH=1")
	hideChild(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("the PS5Upload engine wouldn't start: %v", err)
	}
	go cmd.Wait()
	p5uProc, p5uStdin = cmd, in
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 60; i++ {
		if p5uPing(base) {
			p5uBase = base
			return base, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	cmd.Process.Kill()
	return "", errors.New("the PS5Upload engine started but didn't answer")
}

func p5uPing(base string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	r, err := c.Get(base + "/api/ava1/identity")
	if err != nil {
		return false
	}
	r.Body.Close()
	return r.StatusCode == 200
}

func p5uCall(method, u string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := p5uHTTP.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if r.StatusCode >= 400 {
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// p5uHelperUp reports whether PS5Upload's helper is answering on the PS5.
func p5uHelperUp(base, host string) bool {
	if os.Getenv("TT_P5UREL") != "" { // test stand-in: no status call
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, "9120"), 2*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	var st map[string]any
	if err := p5uCall("GET", base+"/api/ps5/status?addr="+url.QueryEscape(host), nil, &st); err != nil {
		return false
	}
	_, bad := st["error"]
	return !bad
}

// p5uReady gets the engine running and the helper loaded on the PS5.
func p5uReady(host string, cancelled func() bool) (string, error) {
	base, err := p5uStart()
	if err != nil {
		return "", err
	}
	p5uHelp.Lock()
	defer p5uHelp.Unlock()
	if p5uHelperUp(base, host) {
		return base, nil
	}
	// a PS5 that just refused the helper isn't asked again for a few minutes,
	// so each game doesn't wait on it before falling back to FTP
	if t, ok := p5uBad[host]; ok && time.Since(t.at) < 3*time.Minute {
		return "", t.err
	}
	fail := func(err error) (string, error) {
		p5uBad[host] = p5uFail{time.Now(), err}
		return "", err
	}
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := p5uCall("POST", base+"/api/pkg/payload-restore", map[string]string{"ps5_addr": host}, &res); err != nil {
		return "", err
	}
	if !res.OK {
		return fail(fmt.Errorf("couldn't load the PS5Upload helper on the PS5 (%s). The PS5 needs its ELF loader running on port 9021", res.Error))
	}
	for i := 0; i < 50; i++ {
		if cancelled != nil && cancelled() {
			return "", errFTPCancelled
		}
		time.Sleep(500 * time.Millisecond)
		if p5uHelperUp(base, host) {
			return base, nil
		}
	}
	return fail(errors.New("the PS5Upload helper was sent to the PS5 but didn't start answering"))
}

type p5uJob struct {
	Status     string `json:"status"`
	BytesSent  int64  `json:"bytes_sent"`
	TotalBytes int64  `json:"total_bytes"`
	Bottleneck string `json:"bottleneck"`
	Error      string `json:"error"`
	Reason     string `json:"error_reason"`
}

// p5uSend sends one file through PS5Upload. started is false when PS5Upload
// couldn't be used at all (so the caller can fall back to FTP), and true once
// the transfer was handed over, whatever came of it.
func p5uSend(host, local, remote string, progress func(sent, total int64), slow func(string), cancelled func() bool) (started bool, err error) {
	base, err := p5uReady(host, cancelled)
	if err != nil {
		return false, err
	}
	if os.Getenv("TT_P5UREL") != "" {
		remote = strings.TrimPrefix(remote, "/")
	}
	var open struct {
		JobID string `json:"job_id"`
	}
	if err := p5uCall("POST", base+"/api/transfer/file", map[string]string{"addr": host, "src": local, "dest": remote}, &open); err != nil {
		return false, err
	}
	if open.JobID == "" {
		return false, errors.New("the PS5Upload engine didn't start the transfer")
	}
	fi, _ := os.Stat(local)
	misses := 0
	for {
		time.Sleep(300 * time.Millisecond)
		if cancelled != nil && cancelled() {
			p5uCall("POST", base+"/api/jobs/"+open.JobID+"/cancel", map[string]string{}, nil)
			return true, errFTPCancelled
		}
		var j p5uJob
		if err := p5uCall("GET", base+"/api/jobs/"+open.JobID, nil, &j); err != nil {
			if misses++; misses > 20 {
				return true, fmt.Errorf("lost contact with the PS5Upload engine (%v)", err)
			}
			continue
		}
		misses = 0
		switch j.Status {
		case "running":
			total := j.TotalBytes
			if total <= 0 && fi != nil {
				total = fi.Size()
			}
			if progress != nil && total > 0 {
				sent := j.BytesSent
				if sent >= total {
					sent = total - 1 // the last step is the PS5 checking the file
				}
				progress(sent, total)
			}
			if slow != nil {
				slow(j.Bottleneck)
			}
		case "done":
			if progress != nil && fi != nil {
				progress(fi.Size(), fi.Size())
			}
			return true, nil
		case "failed":
			msg := j.Error
			if msg == "" {
				msg = "the transfer failed"
			}
			if strings.Contains(strings.ToLower(msg+" "+j.Reason), "cancel") {
				return true, errFTPCancelled
			}
			return true, errors.New("PS5Upload: " + msg)
		default:
			return true, fmt.Errorf("PS5Upload ended the transfer (%s)", j.Status)
		}
	}
}

// p5uStop shuts the engine down when Loadout quits.
func p5uStop() {
	p5uMu.Lock()
	defer p5uMu.Unlock()
	if p5uStdin != nil {
		p5uStdin.Close()
	}
	if p5uProc != nil && p5uProc.Process != nil {
		p5uProc.Process.Kill()
	}
}
