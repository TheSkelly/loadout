package main

// ftp.go: a small FTP client for sending images to the PS5's FTP server
// (ftpsrv on port 2121 or etaHEN on 1337). Uploads go to "<name>.part"
// first and are renamed when complete, so nothing on the console ever
// sees a half-sent image. Interrupted uploads resume where they stopped.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
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

type FTPConfig struct {
	Host string
	Port int
	User string
	Pass string
}

type ftpConn struct {
	c       net.Conn
	r       *bufio.Reader
	host    string
	cfg     FTPConfig
	banner  string // the server's greeting
	lastRaw string // start of the last folder listing, as the server sent it
	feat    map[string]bool
	hurry   bool  // winding down after Stop: don't wait long for answers
	slow    int32 // during an upload: 0 fine, 1 this PC's drive is slow, 2 the PS5 side is slow
}

var errFTPCancelled = errors.New("cancelled")

// ftpEntry is one item in a PS5 folder listing.
type ftpEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// ftpDial connects anonymously, which is how PS5 FTP payloads work.
func ftpDial(host string, port int) (*ftpConn, error) {
	return ftpDialCfg(FTPConfig{Host: host, Port: port})
}

func ftpDialCfg(cfg FTPConfig) (*ftpConn, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, errors.New("Set your PS5's IP address in Settings first.")
	}
	port := cfg.Port
	if port == 0 {
		port = 2121
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	c, err := net.DialTimeout("tcp", addr, 6*time.Second)
	if err != nil {
		return nil, fmt.Errorf("Couldn't reach the PS5 at %s. Check the IP, that the PS5 is on, and that its FTP server is running (%v)", addr, shortNetErr(err))
	}
	f := &ftpConn{c: c, r: bufio.NewReader(c), host: host, cfg: cfg, feat: map[string]bool{}}
	code, msg, err := f.read()
	f.banner = msg
	if err != nil || code != 220 {
		c.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return nil, fmt.Errorf("The PS5's FTP server didn't answer properly: %v", err)
	}
	user := cfg.User
	if user == "" {
		user = "anonymous"
	}
	code, msg, err = f.cmd("USER %s", user)
	if err == nil && code == 331 {
		pass := cfg.Pass
		if pass == "" {
			pass = "anonymous"
		}
		code, msg, err = f.cmd("PASS %s", pass)
	}
	if err != nil || (code != 230 && code != 202) {
		f.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return nil, fmt.Errorf("The PS5's FTP server refused the login: %v", err)
	}
	if code, msg, err := f.cmd("FEAT"); err == nil && code == 211 {
		for _, l := range strings.Split(msg, "\n") {
			if w := strings.Fields(l); len(w) > 0 {
				f.feat[strings.ToUpper(w[0])] = true
			}
		}
	}
	f.cmd("TYPE I")
	// ftpsrv starts every connection in a mode where signed program files
	// (eboot.bin and the like) are reported and sent as their unpacked form,
	// with a different size. Loadout wants files exactly as they are, so it
	// switches that off. SELF is a toggle: if it answers "enabled", flip it back.
	if strings.Contains(strings.ToLower(f.banner), "ftpsrv") {
		if _, msg, err := f.cmd("SELF"); err == nil && strings.Contains(strings.ToLower(msg), "enabled") {
			f.cmd("SELF")
		}
	}
	f.cmd("OPTS UTF8 ON")
	return f, nil
}

func shortNetErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "refused"):
		return "connection refused"
	case strings.Contains(s, "timeout"), strings.Contains(s, "i/o timeout"):
		return "no answer"
	case strings.Contains(s, "no route"), strings.Contains(s, "unreachable"):
		return "network unreachable"
	case strings.Contains(s, "reset"), strings.Contains(s, "broken pipe"), strings.Contains(s, "forcibly closed"), strings.Contains(s, "aborted"):
		return "the PS5 closed the connection"
	}
	return s
}

func (f *ftpConn) Close() {
	f.c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(f.c, "QUIT\r\n")
	f.c.Close()
}

// read returns one (possibly multi-line) reply.
func (f *ftpConn) read() (int, string, error) {
	wait := 30 * time.Second
	if f.hurry {
		wait = 3 * time.Second
	}
	f.c.SetReadDeadline(time.Now().Add(wait))
	line, err := f.r.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 3 {
		return 0, line, errors.New("bad reply: " + line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, line, errors.New("bad reply: " + line)
	}
	msg := strings.TrimSpace(line[3:])
	if len(line) > 3 && line[3] == '-' {
		var b strings.Builder
		b.WriteString(strings.TrimSpace(line[4:]))
		for {
			l, err := f.r.ReadString('\n')
			if err != nil {
				return 0, "", err
			}
			l = strings.TrimRight(l, "\r\n")
			if strings.HasPrefix(l, line[:3]+" ") {
				break
			}
			b.WriteString("\n" + strings.TrimSpace(l))
		}
		msg = b.String()
	}
	return code, msg, nil
}

func (f *ftpConn) cmd(format string, args ...any) (int, string, error) {
	f.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err := fmt.Fprintf(f.c, format+"\r\n", args...); err != nil {
		return 0, "", err
	}
	return f.read()
}

var pasvRe = regexp.MustCompile(`(\d+),(\d+),(\d+),(\d+),(\d+),(\d+)`)
var epsvRe = regexp.MustCompile(`\(\|\|\|(\d+)\|\)`)

// data opens a passive data connection.
func (f *ftpConn) data() (net.Conn, error) {
	var addr string
	if code, msg, err := f.cmd("EPSV"); err == nil && code == 229 {
		if m := epsvRe.FindStringSubmatch(msg); m != nil {
			addr = net.JoinHostPort(f.host, m[1])
		}
	}
	if addr == "" {
		code, msg, err := f.cmd("PASV")
		if err != nil {
			return nil, err
		}
		m := pasvRe.FindStringSubmatch(msg)
		if code != 227 || m == nil {
			return nil, errors.New("the server won't open a data connection: " + msg)
		}
		ip := strings.Join(m[1:5], ".")
		if ip == "0.0.0.0" || strings.HasPrefix(ip, "127.") {
			ip = f.host
		}
		p1, _ := strconv.Atoi(m[5])
		p2, _ := strconv.Atoi(m[6])
		addr = net.JoinHostPort(ip, strconv.Itoa(p1*256+p2))
	}
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if tc, ok := c.(*net.TCPConn); ok && err == nil {
		// big buffers keep a single upload moving over Wi-Fi and other
		// higher-latency links
		tc.SetWriteBuffer(8 << 20)
		tc.SetReadBuffer(4 << 20)
	}
	return c, err
}

func (f *ftpConn) size(p string) (int64, bool) {
	code, msg, err := f.cmd("SIZE %s", p)
	if err != nil || code != 213 {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(msg), 10, 64)
	return n, err == nil
}

func (f *ftpConn) mkdirAll(dir string) error {
	dir = path.Clean("/" + strings.ReplaceAll(dir, "\\", "/"))
	cur := ""
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" {
			continue
		}
		cur += "/" + part
		if code, _, err := f.cmd("CWD %s", cur); err == nil && ok2xx(code) {
			continue
		}
		code, msg, err := f.cmd("MKD %s", cur)
		if err != nil {
			return err
		}
		if !ok2xx(code) {
			if c2, _, _ := f.cmd("CWD %s", cur); !ok2xx(c2) {
				return fmt.Errorf("couldn't create %s on the PS5: %s", cur, msg)
			}
		}
	}
	f.cmd("CWD /")
	return nil
}

// list returns what's inside dir. It fails if dir doesn't exist.
func (f *ftpConn) list(dir string) ([]ftpEntry, error) {
	dir = path.Clean("/" + strings.ReplaceAll(dir, "\\", "/"))
	if code, msg, err := f.cmd("CWD %s", dir); err != nil || !ok2xx(code) {
		if err == nil {
			err = errors.New(msg)
		}
		return nil, fmt.Errorf("%s isn't on the PS5: %v", dir, err)
	}
	// List the folder we just stepped into, without naming it again. Some
	// servers (ftpsrv 0.21 with MLSD) ignore a folder name on the listing
	// command and list wherever the connection currently is.
	defer f.cmd("CWD /")
	useMLSD := f.feat["MLST"] || f.feat["MLSD"]
	d, err := f.data()
	if err != nil {
		return nil, err
	}
	verb := "LIST"
	if useMLSD {
		verb = "MLSD"
	}
	code, msg, err := f.cmd("%s", verb)
	if err != nil || (code != 150 && code != 125) {
		d.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return nil, fmt.Errorf("couldn't list %s: %v", dir, err)
	}
	d.SetReadDeadline(time.Now().Add(30 * time.Second))
	raw, rerr := io.ReadAll(io.LimitReader(d, 8<<20))
	d.Close()
	f.read() // 226
	f.lastRaw = string(raw)
	if len(f.lastRaw) > 600 {
		f.lastRaw = f.lastRaw[:600]
	}
	if rerr != nil {
		// a busy PS5 can time out mid-list; an empty answer here would look like an empty folder
		return nil, fmt.Errorf("the PS5 was too slow listing %s: %v", dir, shortNetErr(rerr))
	}
	var out []ftpEntry
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		var e ftpEntry
		if useMLSD {
			i := strings.Index(l, " ")
			if i < 0 {
				continue
			}
			e.Name = l[i+1:]
			for _, fact := range strings.Split(strings.ToLower(l[:i]), ";") {
				kv := strings.SplitN(fact, "=", 2)
				if len(kv) != 2 {
					continue
				}
				switch kv[0] {
				case "type":
					if kv[1] == "cdir" || kv[1] == "pdir" {
						e.Name = ""
					}
					e.IsDir = kv[1] == "dir"
				case "size":
					e.Size, _ = strconv.ParseInt(kv[1], 10, 64)
				}
			}
		} else {
			// unix style: drwxr-xr-x 1 user group size Mon DD HH:MM name
			fields := strings.Fields(l)
			if len(fields) < 9 {
				continue
			}
			e.IsDir = l[0] == 'd'
			e.Size, _ = strconv.ParseInt(fields[4], 10, 64)
			rest := l
			for n := 0; n < 8; n++ {
				rest = strings.TrimLeft(rest, " ")
				k := strings.Index(rest, " ")
				if k < 0 {
					rest = ""
					break
				}
				rest = rest[k:]
			}
			e.Name = strings.TrimLeft(rest, " ")
		}
		if e.Name == "" || e.Name == "." || e.Name == ".." {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

type ftpProgress func(sent, total int64)

// upload sends local into remoteDir under its own file name. A copy of the
// same size already on the PS5 is left alone (progress reports it as fully
// sent). An interrupted earlier upload resumes; resumed reports that.
func (f *ftpConn) upload(local, remoteDir string, progress ftpProgress, cancelled func() bool) (resumed bool, err error) {
	return f.uploadAs(local, remoteDir, filepath.Base(local), false, progress, cancelled)
}

func (f *ftpConn) uploadAs(local, remoteDir, name string, overwrite bool, progress ftpProgress, cancelled func() bool) (resumed bool, err error) {
	src, err := os.Open(local)
	if err != nil {
		return false, err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return false, err
	}
	total := fi.Size()
	remoteDir = path.Clean("/" + strings.ReplaceAll(remoteDir, "\\", "/"))
	if err := f.mkdirAll(remoteDir); err != nil {
		return false, err
	}
	final := path.Join(remoteDir, name)
	part := final + ".part"
	f.waitPart(part)
	if n, ok := f.size(final); ok {
		switch {
		case n == total && !overwrite:
			if progress != nil {
				progress(total, total)
			}
			return false, nil
		case n < total && f.feat["REST"]:
			// a cut-off copy under the final name: continue it as .part
			f.cmd("DELE %s", part)
			f.cmd("RNFR %s", final)
			f.cmd("RNTO %s", part)
		}
	}
	var start int64
	if n, ok := f.size(part); ok && n > 0 && n < total && f.feat["REST"] {
		start = n
	}
	d, err := f.data()
	if err != nil {
		return false, fmt.Errorf("couldn't open a data connection to the PS5: %v", err)
	}
	if start > 0 {
		if code, _, err := f.cmd("REST %d", start); err != nil || code != 350 {
			start = 0
		}
	}
	resumed = start > 0
	if _, err := src.Seek(start, io.SeekStart); err != nil {
		d.Close()
		return false, err
	}
	code, msg, err := f.cmd("STOR %s", part)
	if err != nil || (code != 150 && code != 125) {
		d.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return false, fmt.Errorf("the PS5 won't accept the file: %v", err)
	}
	// Stop closes the data connection, so a stalled write ends right away
	watchDone := make(chan struct{})
	defer close(watchDone)
	if cancelled != nil {
		go func() {
			t := time.NewTicker(250 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-watchDone:
					return
				case <-t.C:
					if cancelled() {
						d.Close()
						return
					}
				}
			}
		}()
	}
	buf := make([]byte, 1<<20)
	sent := start
	var werr error
	var rdDur, wrDur time.Duration
	var winBytes int64
	winAt := time.Now()
	for {
		if cancelled != nil && cancelled() {
			werr = errFTPCancelled
			break
		}
		t0 := time.Now()
		n, rerr := src.Read(buf)
		rdDur += time.Since(t0)
		if n > 0 {
			d.SetWriteDeadline(time.Now().Add(60 * time.Second))
			t1 := time.Now()
			_, err := d.Write(buf[:n])
			wrDur += time.Since(t1)
			winBytes += int64(n)
			// every few seconds, work out which side is holding things up
			if dt := time.Since(winAt); dt >= 3*time.Second {
				why := int32(0)
				if float64(winBytes)/dt.Seconds() < 3<<20 {
					why = 2
					if rdDur > wrDur {
						why = 1
					}
				}
				atomic.StoreInt32(&f.slow, why)
				winAt, winBytes, rdDur, wrDur = time.Now(), 0, 0, 0
			}
			if err != nil {
				if cancelled != nil && cancelled() {
					werr = errFTPCancelled
					break
				}
				werr = fmt.Errorf("the connection to the PS5 dropped (%v). Send again to pick up where it left off", shortNetErr(err))
				break
			}
			sent += int64(n)
			if progress != nil {
				progress(sent, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			werr = rerr
			break
		}
	}
	d.Close()
	if werr != nil {
		if werr == errFTPCancelled {
			// stopped on purpose: let go at once, and tidy the half-sent file
			// up in the background so Stop never hangs on a busy PS5
			f.hurry = true
			f.cmd("ABOR")
			f.deleteLater(part)
			return resumed, werr
		}
		f.cmd("ABOR")
		f.read()
		return resumed, werr
	}
	if code, msg, err := f.read(); err != nil || !ok2xx(code) {
		if err == nil {
			err = errors.New(msg)
		}
		return false, fmt.Errorf("the PS5 didn't confirm the upload: %v", err)
	}
	if n, ok := f.size(part); ok && n != total {
		return false, fmt.Errorf("the PS5 has %s of %s, the upload is incomplete", humanSize(n), humanSize(total))
	}
	if _, ok := f.size(final); ok {
		f.cmd("DELE %s", final)
	}
	if code, _, err := f.cmd("RNFR %s", part); err == nil && code == 350 {
		if code, msg, err := f.cmd("RNTO %s", final); err != nil || !ok2xx(code) {
			if err == nil {
				err = errors.New(msg)
			}
			return false, fmt.Errorf("uploaded, but couldn't rename %s.part on the PS5: %v", name, err)
		}
	} else {
		return false, fmt.Errorf("uploaded, but the PS5 won't rename %s.part. Rename it by hand", name)
	}
	return resumed, nil
}

// deleteLater removes a file over a fresh connection (the current one may
// be out of step after an aborted transfer). The PS5 can take a moment to
// close the file, so it tries a few times.
var (
	partMu   sync.Mutex
	partBusy = map[string]chan struct{}{}
)

// waitPart waits (a short while) for a half-sent file from a stopped send to
// be cleared away, so a new send of the same file doesn't trip over it.
func (f *ftpConn) waitPart(p string) {
	partMu.Lock()
	ch := partBusy[f.host+p]
	partMu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-time.After(40 * time.Second):
		}
	}
}

// deleteLater removes a half-sent file in the background, over a new
// connection, trying a few times while the PS5 lets go of it.
func (f *ftpConn) deleteLater(p string) {
	key, cfg := f.host+p, f.cfg
	ch := make(chan struct{})
	partMu.Lock()
	if partBusy[key] != nil {
		partMu.Unlock()
		return
	}
	partBusy[key] = ch
	partMu.Unlock()
	go func() {
		defer func() {
			partMu.Lock()
			delete(partBusy, key)
			partMu.Unlock()
			close(ch)
		}()
		for try := 0; try < 4; try++ {
			if try > 0 {
				time.Sleep(time.Duration(try) * 400 * time.Millisecond)
			}
			g, err := ftpDialCfg(cfg)
			if err != nil {
				continue
			}
			g.hurry = true
			code, _, err := g.cmd("DELE %s", p)
			if err == nil && ok2xx(code) {
				g.Close()
				return
			}
			_, still := g.size(p)
			g.Close()
			if !still {
				return
			}
		}
	}()
}

// removePartials deletes unfinished uploads (*.exfat.part) in dir and
// returns their names.
func (f *ftpConn) removePartials(dir string) ([]string, error) {
	ents, err := f.list(dir)
	if err != nil {
		return nil, err
	}
	dir = path.Clean("/" + strings.ReplaceAll(dir, "\\", "/"))
	var gone []string
	for _, e := range ents {
		if e.IsDir || !strings.HasSuffix(strings.ToLower(e.Name), ".part") {
			continue
		}
		if code, msg, err := f.cmd("DELE %s", path.Join(dir, e.Name)); err != nil || !ok2xx(code) {
			if err == nil {
				err = errors.New(msg)
			}
			return gone, fmt.Errorf("couldn't remove %s: %v", e.Name, err)
		}
		gone = append(gone, strings.TrimSuffix(e.Name, ".part"))
	}
	return gone, nil
}

// speedTest uploads throwaway data to dir for up to dur (or max bytes),
// deletes it, and returns the measured speed in bytes per second.
func (f *ftpConn) speedTest(dir string, dur time.Duration, max int64) (float64, error) {
	dir = path.Clean("/" + strings.ReplaceAll(dir, "\\", "/"))
	if err := f.mkdirAll(dir); err != nil {
		return 0, err
	}
	name := path.Join(dir, ".titletag-speedtest.part")
	d, err := f.data()
	if err != nil {
		return 0, fmt.Errorf("couldn't open a data connection to the PS5: %v", err)
	}
	code, msg, err := f.cmd("STOR %s", name)
	if err != nil || (code != 150 && code != 125) {
		d.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return 0, fmt.Errorf("the PS5 won't accept the test file: %v", err)
	}
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = byte(i*131 + i>>9) // not all zeros, in case anything compresses
	}
	var sent int64
	start := time.Now()
	for time.Since(start) < dur && sent < max {
		d.SetWriteDeadline(time.Now().Add(20 * time.Second))
		n, err := d.Write(buf)
		sent += int64(n)
		if err != nil {
			d.Close()
			f.deleteLater(name)
			return 0, fmt.Errorf("the connection dropped during the test (%v)", shortNetErr(err))
		}
	}
	d.Close()
	f.read() // 226 for the finished upload
	el := time.Since(start).Seconds()
	f.deleteLater(name)
	if el <= 0 {
		return 0, errors.New("the test was too short to measure")
	}
	return float64(sent) / el, nil
}

// get downloads a small file from the PS5 into memory.
func (f *ftpConn) get(p string, max int64) ([]byte, error) {
	f.cmd("TYPE I")
	d, err := f.data()
	if err != nil {
		return nil, err
	}
	code, msg, err := f.cmd("RETR %s", p)
	if err != nil || (code != 150 && code != 125) {
		d.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return nil, err
	}
	d.SetReadDeadline(time.Now().Add(20 * time.Second))
	b, err := io.ReadAll(io.LimitReader(d, max))
	d.Close()
	f.read() // 226
	return b, err
}

// ok2xx: any 2xx reply means the command worked. Servers disagree on the
// exact number (ftpsrv answers 226 where others say 250 or 257).
func ok2xx(code int) bool { return code >= 200 && code < 300 }

// put writes data straight onto an existing file's path (no .part step), so
// the file keeps its place and its permissions on the PS5.
func (f *ftpConn) put(p string, data []byte) error {
	f.cmd("TYPE I")
	d, err := f.data()
	if err != nil {
		return err
	}
	code, msg, err := f.cmd("STOR %s", p)
	if err != nil || (code != 150 && code != 125) {
		d.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return err
	}
	d.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, werr := d.Write(data)
	d.Close()
	code, msg, err = f.read()
	if werr != nil {
		return werr
	}
	if err != nil {
		return err
	}
	if !ok2xx(code) {
		return errors.New(msg)
	}
	return nil
}

// uploadStream sends data that arrives as a stream (a file dropped onto the
// window) to the PS5, with the same .part-then-rename steps as a file upload.
func (f *ftpConn) uploadStream(src io.Reader, total int64, remoteDir, name string, progress ftpProgress, cancelled func() bool) error {
	remoteDir = path.Clean("/" + strings.ReplaceAll(remoteDir, "\\", "/"))
	if err := f.mkdirAll(remoteDir); err != nil {
		return err
	}
	final := path.Join(remoteDir, name)
	part := final + ".part"
	f.waitPart(part)
	f.cmd("DELE %s", part)
	d, err := f.data()
	if err != nil {
		return fmt.Errorf("couldn't open a data connection to the PS5: %v", err)
	}
	code, msg, err := f.cmd("STOR %s", part)
	if err != nil || (code != 150 && code != 125) {
		d.Close()
		if err == nil {
			err = errors.New(msg)
		}
		return fmt.Errorf("the PS5 won't accept the file: %v", err)
	}
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-t.C:
				if cancelled != nil && cancelled() {
					d.Close()
					return
				}
			}
		}
	}()
	buf := make([]byte, 1<<20)
	var sent int64
	var werr error
	for {
		if cancelled != nil && cancelled() {
			werr = errFTPCancelled
			break
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			d.SetWriteDeadline(time.Now().Add(60 * time.Second))
			if _, err := d.Write(buf[:n]); err != nil {
				if cancelled != nil && cancelled() {
					werr = errFTPCancelled
				} else {
					werr = fmt.Errorf("the connection to the PS5 dropped (%v)", shortNetErr(err))
				}
				break
			}
			sent += int64(n)
			if progress != nil {
				progress(sent, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			werr = fmt.Errorf("the file stopped arriving from the window (%v)", rerr)
			break
		}
	}
	d.Close()
	if werr != nil {
		f.hurry = true
		f.cmd("ABOR")
		f.deleteLater(part)
		return werr
	}
	if code, msg, err := f.read(); err != nil || !ok2xx(code) {
		if err == nil {
			err = errors.New(msg)
		}
		return fmt.Errorf("the PS5 didn't confirm the upload: %v", err)
	}
	if total > 0 && sent != total {
		f.deleteLater(part)
		return fmt.Errorf("only %s of %s arrived, so the file was not kept", humanSize(sent), humanSize(total))
	}
	if n, ok := f.size(part); ok && n != sent {
		return fmt.Errorf("the PS5 has %s of %s, the upload is incomplete", humanSize(n), humanSize(sent))
	}
	if _, ok := f.size(final); ok {
		f.cmd("DELE %s", final)
	}
	if code, _, err := f.cmd("RNFR %s", part); err == nil && code == 350 {
		if code, msg, err := f.cmd("RNTO %s", final); err != nil || !ok2xx(code) {
			if err == nil {
				err = errors.New(msg)
			}
			return fmt.Errorf("uploaded, but couldn't rename %s.part on the PS5: %v", name, err)
		}
		return nil
	}
	return fmt.Errorf("uploaded, but the PS5 won't rename %s.part. Rename it by hand", name)
}
