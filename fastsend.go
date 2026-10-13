package main

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Loadout's own receiver for the PS5. It lets one image travel over several
// connections at once and reserves the file up front, which FTP can't do.

//go:embed ps5recv/ttrecv.elf
var recvELF []byte

const (
	recvPort   = 9046
	recvVer    = 3
	segAlign   = 8 << 20
	segMinSize = 256 << 20
	// One connection writes the file front to back. More than one writes it in
	// slices out of order, which is opt-in until it is proven on a real PS5.
	defaultStreams = 1
)

func recvAddr(host string) string {
	if p := os.Getenv("TT_RECVPORT"); p != "" { // test stand-in
		return net.JoinHostPort(host, p)
	}
	return net.JoinHostPort(host, strconv.Itoa(recvPort))
}

func recvHeader(op byte, p string, off, length, total int64) []byte {
	h := make([]byte, 32, 32+len(p))
	copy(h, "TTRX")
	h[4], h[5] = recvVer, op
	binary.LittleEndian.PutUint16(h[6:], uint16(len(p)))
	binary.LittleEndian.PutUint64(h[8:], uint64(off))
	binary.LittleEndian.PutUint64(h[16:], uint64(length))
	binary.LittleEndian.PutUint64(h[24:], uint64(total))
	return append(h, p...)
}

// recvPing reports whether the receiver is running on the PS5.
func recvPing(host string) bool {
	c, err := net.DialTimeout("tcp", recvAddr(host), 1500*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(recvHeader(2, "", 0, 0, 0)); err != nil {
		return false
	}
	b := make([]byte, 5)
	_, err = io.ReadFull(c, b)
	return err == nil && string(b[:4]) == "TTRX" && b[4] == recvVer
}

// ensureReceiver makes sure the receiver runs, loading it through the PS5's
// ELF loader when it isn't there yet.
func (a *App) ensureReceiver(host string, cancelled func() bool) bool {
	if recvPing(host) {
		return true
	}
	// if it didn't load a moment ago, don't hold up every send trying again
	recvMu.Lock()
	recent := time.Since(recvFailed[host]) < 10*time.Minute
	recvFailed[host] = time.Now()
	recvMu.Unlock()
	if recent {
		return false
	}
	a.mu.Lock()
	port := a.loaderPortLocked()
	a.mu.Unlock()
	if sendPayload(host, port, recvELF) != nil {
		return false
	}
	for i := 0; i < 16; i++ {
		time.Sleep(300 * time.Millisecond)
		if cancelled != nil && cancelled() {
			return false
		}
		if recvPing(host) {
			recvMu.Lock()
			delete(recvFailed, host)
			recvMu.Unlock()
			return true
		}
	}
	return false
}

var (
	recvMu     sync.Mutex
	recvFailed = map[string]time.Time{}
)

type fastSeg struct {
	Off, Len, Done int64
}

type fastState struct {
	Size  int64
	MTime int64
	Segs  []*fastSeg
}

func fastStateName(host, part string) string {
	h := fnv.New64a()
	h.Write([]byte(host + "|" + part))
	return fmt.Sprintf("resume-%x.json", h.Sum64())
}

func loadFastState(host, part string, fi os.FileInfo) *fastState {
	var st fastState
	loadJSON(fastStateName(host, part), &st)
	if st.Size != fi.Size() || st.MTime != fi.ModTime().Unix() || len(st.Segs) == 0 {
		return nil
	}
	return &st
}

func dropFastState(host, part string) {
	os.Remove(filepath.Join(appDir(), fastStateName(host, part)))
}

// hasFastResume reports whether a cut-off fast send of img is waiting in dir.
func hasFastResume(host, dir, img string) bool {
	fi, err := os.Stat(img)
	if err != nil {
		return false
	}
	return loadFastState(host, path.Join(path.Clean("/"+strings.ReplaceAll(dir, "\\", "/")), filepath.Base(img))+".part", fi) != nil
}

// manyStreamsOK: slices land out of order, which is only safe to do on the
// PS5's own file system. USB drives (exFAT) get one stream, written in order.
func manyStreamsOK(dir string) bool {
	return strings.HasPrefix(dir, "/data/") || strings.HasPrefix(dir, "/user/") || dir == "/data"
}

func splitSegs(start, total int64, streams int) []*fastSeg {
	left := total - start
	if n := int(left / segMinSize); n < streams {
		streams = n
	}
	if streams < 1 {
		streams = 1
	}
	var segs []*fastSeg
	per := (left/int64(streams) + segAlign - 1) / segAlign * segAlign
	for off := start; off < total; off += per {
		l := per
		if off+l > total || len(segs) == streams-1 {
			l = total - off
		}
		segs = append(segs, &fastSeg{Off: off, Len: l})
		if off+l >= total {
			break
		}
	}
	return segs
}

var errRecvDisk = errors.New("the PS5 couldn't write the file. The drive may be full or unplugged")

// sendSeg streams one slice. seg.Done moves as the PS5 confirms writes.
func sendSeg(host, part string, src *os.File, seg *fastSeg, total int64, stop *int32, reg func(net.Conn)) error {
	base := atomic.LoadInt64(&seg.Done)
	if base >= seg.Len {
		return nil
	}
	c, err := net.DialTimeout("tcp", recvAddr(host), 10*time.Second)
	if err != nil {
		return fmt.Errorf("couldn't reach the receiver on the PS5 (%v)", shortNetErr(err))
	}
	defer c.Close()
	reg(c)
	if atomic.LoadInt32(stop) != 0 {
		return errFTPCancelled
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetWriteBuffer(4 << 20)
	}
	want := seg.Len - base
	c.SetDeadline(time.Now().Add(5 * time.Minute)) // reserving a big file can take a while
	if _, err := c.Write(recvHeader(1, part, seg.Off+base, want, total)); err != nil {
		return fmt.Errorf("the connection to the PS5 dropped (%v)", shortNetErr(err))
	}
	var rep [8]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return fmt.Errorf("the PS5 didn't answer (%v)", shortNetErr(err))
	}
	if binary.LittleEndian.Uint32(rep[:4]) != 0 {
		return fmt.Errorf("the PS5 won't open the file (error %d)", binary.LittleEndian.Uint32(rep[4:]))
	}
	c.SetDeadline(time.Time{})

	ackErr := make(chan error, 1)
	go func() {
		var b [8]byte
		for {
			if _, err := io.ReadFull(c, b[:]); err != nil {
				ackErr <- fmt.Errorf("the connection to the PS5 dropped (%v)", shortNetErr(err))
				return
			}
			n := binary.LittleEndian.Uint64(b[:])
			if n == math.MaxUint64 {
				ackErr <- errRecvDisk
				return
			}
			atomic.StoreInt64(&seg.Done, base+int64(n))
			if int64(n) >= want {
				ackErr <- nil
				return
			}
		}
	}()

	buf := make([]byte, 1<<20)
	for sent := int64(0); sent < want; {
		if atomic.LoadInt32(stop) != 0 {
			return errFTPCancelled
		}
		select {
		case err := <-ackErr:
			if err == nil {
				err = errors.New("the PS5 stopped early")
			}
			return err
		default:
		}
		n := int64(len(buf))
		if want-sent < n {
			n = want - sent
		}
		if _, err := src.ReadAt(buf[:n], seg.Off+base+sent); err != nil {
			return err
		}
		c.SetWriteDeadline(time.Now().Add(90 * time.Second))
		if _, err := c.Write(buf[:n]); err != nil {
			select {
			case aerr := <-ackErr:
				if aerr != nil {
					return aerr
				}
			case <-time.After(300 * time.Millisecond):
			}
			return fmt.Errorf("the connection to the PS5 dropped (%v)", shortNetErr(err))
		}
		sent += n
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Minute))
	return <-ackErr
}

// fastUpload sends a file through the receiver. It keeps the same .part and
// rename steps as the FTP upload, and uses FTP for those small jobs.
func fastUpload(f *ftpConn, host, local, remoteDir string, streams int, progress ftpProgress, cancelled func() bool) (resumed bool, used int, err error) {
	src, err := os.Open(local)
	if err != nil {
		return false, 0, err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return false, 0, err
	}
	total := fi.Size()
	name := filepath.Base(local)
	remoteDir = path.Clean("/" + strings.ReplaceAll(remoteDir, "\\", "/"))
	if err := f.mkdirAll(remoteDir); err != nil {
		return false, 0, err
	}
	final := path.Join(remoteDir, name)
	part := final + ".part"
	f.waitPart(part)
	if n, ok := f.size(final); ok {
		if n == total {
			progress(total, total)
			return false, 0, nil
		}
		if n < total {
			// a cut-off copy under the final name: continue it as .part
			f.cmd("DELE %s", part)
			f.cmd("RNFR %s", final)
			f.cmd("RNTO %s", part)
			dropFastState(host, part)
		}
	}
	multi := manyStreamsOK(remoteDir + "/")
	if !multi {
		streams = 1
	}
	psize, pok := f.size(part)
	st := loadFastState(host, part, fi)
	switch {
	case st != nil && pok:
		resumed = true
	case pok && psize > 0 && psize < total && !multi:
		// left behind by a plain FTP upload: the first psize bytes are good
		st = &fastState{Size: total, MTime: fi.ModTime().Unix(), Segs: splitSegs(psize/segAlign*segAlign, total, streams)}
		resumed = st.Segs[0].Off > 0
	default:
		if pok {
			f.cmd("DELE %s", part)
		}
		st = &fastState{Size: total, MTime: fi.ModTime().Unix(), Segs: splitSegs(0, total, streams)}
	}
	// No up-front reserve: on the PS5 that writes the whole file out as zeros
	// first, so every byte gets written twice and the send stalls at the start.
	reserve := int64(0)
	sentNow := func() int64 {
		n := st.Segs[0].Off // bytes that were already there before the first slice
		for _, s := range st.Segs {
			n += atomic.LoadInt64(&s.Done)
		}
		return n
	}
	save := func() {
		cp := fastState{Size: st.Size, MTime: st.MTime}
		for _, s := range st.Segs {
			cp.Segs = append(cp.Segs, &fastSeg{Off: s.Off, Len: s.Len, Done: atomic.LoadInt64(&s.Done)})
		}
		saveJSON(fastStateName(host, part), &cp)
	}
	save()
	progress(sentNow(), total)

	var stop int32
	var wg sync.WaitGroup
	// Stop closes every connection, so nothing sits waiting on the PS5
	var cmu sync.Mutex
	var conns []net.Conn
	reg := func(c net.Conn) { cmu.Lock(); conns = append(conns, c); cmu.Unlock() }
	closeAll := func() {
		cmu.Lock()
		for _, c := range conns {
			c.Close()
		}
		cmu.Unlock()
	}
	errs := make(chan error, len(st.Segs))
	for _, s := range st.Segs {
		wg.Add(1)
		go func(s *fastSeg) {
			defer wg.Done()
			if err := sendSeg(host, part, src, s, reserve, &stop, reg); err != nil {
				atomic.StoreInt32(&stop, 1)
				closeAll()
				errs <- err
			}
		}(s)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	lastSave := time.Now()
	userStop := false
wait:
	for {
		select {
		case <-done:
			break wait
		case <-tick.C:
			if cancelled != nil && cancelled() {
				userStop = true
				atomic.StoreInt32(&stop, 1)
				closeAll()
			}
			progress(sentNow(), total)
			if time.Since(lastSave) > 5*time.Second {
				save()
				lastSave = time.Now()
			}
		}
	}
	used = len(st.Segs)
	if userStop {
		// stopped on purpose: don't leave a half-sent file on the PS5
		dropFastState(host, part)
		f.deleteLater(part)
		return resumed, used, errFTPCancelled
	}
	select {
	case err := <-errs:
		save()
		if errors.Is(err, errRecvDisk) {
			return resumed, used, err
		}
		return resumed, used, fmt.Errorf("%v. Send again to pick up where it left off", err)
	default:
	}
	if sentNow() != total {
		save()
		return resumed, used, errors.New("the PS5 didn't confirm the whole file. Send again to pick up where it left off")
	}
	progress(total, total)
	// the first connection sat idle through the upload; finish on a fresh one
	g, err := ftpDialCfg(f.cfg)
	if err != nil {
		save()
		return resumed, used, fmt.Errorf("uploaded, but the PS5's FTP stopped answering before the rename: %v", err)
	}
	defer g.Close()
	if n, ok := g.size(part); ok && n != total {
		dropFastState(host, part)
		return resumed, used, fmt.Errorf("the PS5 has %s of %s, the upload is incomplete", humanSize(n), humanSize(total))
	}
	if _, ok := g.size(final); ok {
		g.cmd("DELE %s", final)
	}
	if code, _, err := g.cmd("RNFR %s", part); err != nil || code != 350 {
		return resumed, used, fmt.Errorf("uploaded, but the PS5 won't rename %s.part. Rename it by hand", name)
	}
	if code, msg, err := g.cmd("RNTO %s", final); err != nil || !ok2xx(code) {
		if err == nil {
			err = errors.New(msg)
		}
		return resumed, used, fmt.Errorf("uploaded, but couldn't rename %s.part on the PS5: %v", name, err)
	}
	dropFastState(host, part)
	return resumed, used, nil
}

// recvSpeed writes test data through the receiver for a few seconds and
// returns bytes per second. The test file is removed afterwards.
func recvSpeed(host string, f *ftpConn, dir string, streams int, dur time.Duration) (bps float64, used int, sentBytes int64, err error) {
	dir = path.Clean("/" + strings.ReplaceAll(dir, "\\", "/"))
	if err := f.mkdirAll(dir); err != nil {
		return 0, 0, 0, err
	}
	if !manyStreamsOK(dir + "/") {
		streams = 1
	}
	var sentAll int64
	const window = 256 << 20
	name := path.Join(dir, ".loadout-speedtest.part")
	defer f.deleteLater(name)
	var acked int64
	var firstErr atomic.Value
	var wg sync.WaitGroup
	deadline := time.Now().Add(dur)
	start := time.Now()
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", recvAddr(host), 5*time.Second)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			defer c.Close()
			c.SetDeadline(deadline.Add(20 * time.Second))
			if _, err := c.Write(recvHeader(1, name, int64(i)*window, window, 0)); err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			var rep [8]byte
			if _, err := io.ReadFull(c, rep[:]); err != nil || binary.LittleEndian.Uint32(rep[:4]) != 0 {
				firstErr.CompareAndSwap(nil, errors.New("the receiver couldn't open a test file in "+dir))
				return
			}
			go func() {
				var b [8]byte
				var last int64
				for {
					if _, err := io.ReadFull(c, b[:]); err != nil {
						return
					}
					n := int64(binary.LittleEndian.Uint64(b[:]))
					if n < 0 {
						firstErr.CompareAndSwap(nil, errRecvDisk)
						return
					}
					atomic.AddInt64(&acked, n-last)
					last = n
				}
			}()
			buf := make([]byte, 1<<20)
			for sent := int64(0); sent < window && time.Now().Before(deadline); sent += int64(len(buf)) {
				c.SetWriteDeadline(deadline.Add(2 * time.Second))
				if _, err := c.Write(buf); err != nil {
					return
				}
				atomic.AddInt64(&sentAll, int64(len(buf)))
			}
			time.Sleep(1500 * time.Millisecond) // let the last confirmations arrive
		}(i)
	}
	wg.Wait()
	secs := time.Since(start).Seconds()
	n := atomic.LoadInt64(&acked)
	sent := atomic.LoadInt64(&sentAll)
	if n == 0 {
		if err, _ := firstErr.Load().(error); err != nil {
			return 0, streams, sent, err
		}
		return 0, streams, sent, errors.New("no data confirmed")
	}
	return float64(n) / secs, streams, sent, nil
}
