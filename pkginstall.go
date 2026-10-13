package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Installing a PS4 package (.pkg) on the PS5. etaHEN's Direct Package
// Installer listens on port 9090 and takes a web address; the PS5 then
// downloads the package itself and installs it. So Loadout hands the file out
// over a small web server on this PC and tells the PS5 where to fetch it.

const dpiPort = 9090

type pkgShare struct {
	path   string
	size   int64
	served int64 // furthest byte the PS5 has fetched
	hits   int32
	last   int64 // unix time of the last read
}

var (
	pkgMu     sync.Mutex
	pkgLn     net.Listener
	pkgShares = map[string]*pkgShare{}
)

type countingFile struct {
	*os.File
	s *pkgShare
}

func (c *countingFile) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	if n > 0 {
		if pos, e := c.File.Seek(0, io.SeekCurrent); e == nil {
			for {
				old := atomic.LoadInt64(&c.s.served)
				if pos <= old || atomic.CompareAndSwapInt64(&c.s.served, old, pos) {
					break
				}
			}
		}
		atomic.StoreInt64(&c.s.last, time.Now().Unix())
	}
	return n, err
}

// pkgServer starts (once) the little web server packages are fetched from.
func pkgServer() (int, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	if pkgLn != nil {
		return pkgLn.Addr().(*net.TCPAddr).Port, nil
	}
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	pkgLn = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/pkg/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/pkg/"), "/", 2)
		pkgMu.Lock()
		s := pkgShares[parts[0]]
		pkgMu.Unlock()
		if s == nil || (r.Method != "GET" && r.Method != "HEAD") {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(s.path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, _ := f.Stat()
		atomic.AddInt32(&s.hits, 1)
		atomic.StoreInt64(&s.last, time.Now().Unix())
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", fi.ModTime(), &countingFile{f, s})
	})
	go http.Serve(ln, mux)
	return ln.Addr().(*net.TCPAddr).Port, nil
}

var dpiRes = regexp.MustCompile(`"(?:res|result|error|status)"\s*:\s*"?(-?[\w ]+)`)

// startPkgInstall queues a PS4 package from the library for install on the PS5.
func (a *App) startPkgInstall(key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	for _, g := range a.games {
		if g.Path != key || !g.IsPkg {
			continue
		}
		fi, err := os.Stat(g.Path)
		if err != nil {
			return errors.New("That package file isn't there any more.")
		}
		name := g.Name
		if name == "" {
			name = filepath.Base(g.Path)
		}
		j := &BuildJob{Key: key, Kind: "pkginst", Name: name, Out: filepath.Base(g.Path), State: "queued", Size: fi.Size(), image: g.Path}
		a.addJobsLocked([]*BuildJob{j})
		a.enqueuePS5Locked(j)
		return nil
	}
	return errors.New("That isn't a PS4 package in your library.")
}

func (a *App) pkgInstall(j *BuildJob) (string, error) {
	host, _, _ := a.ps5()
	a.mu.Lock()
	j.State, j.Note = "sending", "Asking the PS5 to install it"
	a.mu.Unlock()
	fi, err := os.Stat(j.image)
	if err != nil {
		return "", err
	}
	port, err := pkgServer()
	if err != nil {
		return "", fmt.Errorf("couldn't start the file server on this PC: %v", err)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(dpiPort)), 5*time.Second)
	if err != nil {
		return "", errors.New("the PS5's package installer isn't answering (port 9090). In the etaHEN toolbox on the PS5, turn on the Direct Package Installer (DPI), then try again")
	}
	myIP, _, _ := net.SplitHostPort(c.LocalAddr().String())
	tok := make([]byte, 12)
	rand.Read(tok)
	token := hex.EncodeToString(tok)
	share := &pkgShare{path: j.image, size: fi.Size()}
	pkgMu.Lock()
	pkgShares[token] = share
	pkgMu.Unlock()
	defer func() {
		pkgMu.Lock()
		delete(pkgShares, token)
		pkgMu.Unlock()
	}()
	// a plain file name in the address: installers are picky about odd characters
	link := fmt.Sprintf("http://%s/pkg/%s/%s", net.JoinHostPort(myIP, fmt.Sprint(port)), token, url.PathEscape("game.pkg"))
	c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := fmt.Fprintf(c, `{"url":"%s"}`, link); err != nil {
		c.Close()
		return "", fmt.Errorf("the PS5 dropped the connection: %v", shortNetErr(err))
	}
	reply := make([]byte, 512)
	n, _ := c.Read(reply)
	c.Close()
	if m := dpiRes.FindStringSubmatch(string(reply[:n])); m != nil {
		v := strings.TrimSpace(m[1])
		if v != "0" && !strings.EqualFold(v, "ok") && !strings.EqualFold(v, "success") && !strings.HasPrefix(strings.ToLower(v), "0x0") {
			return "", fmt.Errorf("the PS5 refused the package (it answered %s). It may already be installed, or not be a package this PS5 can install", strings.TrimSpace(string(reply[:n])))
		}
	}
	// the PS5 now downloads the file from us
	start := time.Now()
	prog := a.saveProgress(j, "sending")
	for {
		time.Sleep(300 * time.Millisecond)
		if a.ps5Stopped(j) {
			return "Stopped here. If the PS5 already started the download, cancel it in the PS5's Downloads list", errFTPCancelled
		}
		got := atomic.LoadInt64(&share.served)
		if got > 0 {
			prog(got, share.size)
		}
		if got >= share.size {
			break
		}
		idle := time.Now().Unix() - atomic.LoadInt64(&share.last)
		if atomic.LoadInt32(&share.hits) == 0 && time.Since(start) > 45*time.Second {
			return "", errors.New("the PS5 accepted the request but never fetched the file. Windows Firewall may be blocking Loadout: allow it on private networks, then try again")
		}
		if atomic.LoadInt32(&share.hits) > 0 && idle > 180 {
			return "", errors.New("the PS5 stopped downloading the package partway. Check its Downloads list for an error")
		}
	}
	return "Sent in " + fmtDur(time.Since(start)) + ". The PS5 finishes installing it by itself; watch its Downloads list", nil
}
