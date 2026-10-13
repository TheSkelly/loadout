package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"
)

// Moving a game between PS5 drives is done by the PS5 itself, through the
// shsrv shell's "cp". The shell splits arguments on spaces and can't quote, so
// Loadout gives the file a short temporary name over FTP first:
//
//   1. FTP: rename  <src dir>/<Game>.exfat  ->  <src dir>/ttmove-ID-n.tmp
//   2. shell: cp    <src dir>/ttmove-ID-n.tmp  <dst dir>/ttmove-ID-n.tmp.part
//   3. FTP: check the copy is the full size
//   4. FTP: rename the copy to <dst dir>/<Game>.exfat, then delete the original
//
// If anything goes wrong the partial copy is removed and the original gets its
// name back, so the game is never lost.

// shellConn is a telnet session with shsrv.
type shellConn struct {
	c net.Conn
	r *bufio.Reader
}

func shellOpen(host string) (*shellConn, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(shellPort)), 3*time.Second)
	if err != nil {
		return nil, errors.New("the PS5's shell (shsrv) isn't answering. Load shsrv from Settings › Payloads")
	}
	s := &shellConn{c: c, r: bufio.NewReader(c)}
	if _, err := s.untilPrompt(10*time.Second, nil); err != nil {
		c.Close()
		return nil, errors.New("the PS5's shell didn't give a prompt")
	}
	return s, nil
}

func (s *shellConn) Close() {
	s.c.Write([]byte("exit\r\n"))
	s.c.Close()
}

// untilPrompt reads until the shell prompt returns. idle is called every few
// seconds while waiting; if it returns an error the wait stops with it.
func (s *shellConn) untilPrompt(max time.Duration, idle func() error) (string, error) {
	var text strings.Builder
	start := time.Now()
	isPrompt := func() bool { return strings.HasSuffix(strings.TrimRight(text.String(), " "), "$") }
	for !isPrompt() {
		s.c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b, err := s.r.ReadByte()
		if err != nil {
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				return text.String(), err
			}
			if idle != nil {
				if ierr := idle(); ierr != nil {
					return text.String(), ierr
				}
			}
			if max > 0 && time.Since(start) > max {
				return text.String(), errors.New("timed out")
			}
			continue
		}
		if b != 255 {
			text.WriteByte(b)
			continue
		}
		s.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		cmd, err := s.r.ReadByte()
		if err != nil {
			return text.String(), err
		}
		switch cmd {
		case 251, 252, 253, 254: // WILL WONT DO DONT
			opt, err := s.r.ReadByte()
			if err != nil {
				return text.String(), err
			}
			if cmd == 251 {
				s.c.Write([]byte{255, 254, opt})
			} else if cmd == 253 {
				s.c.Write([]byte{255, 252, opt})
			}
		case 250:
			for {
				x, err := s.r.ReadByte()
				if err != nil {
					return text.String(), err
				}
				if x == 255 {
					if y, _ := s.r.ReadByte(); y == 240 {
						break
					}
				}
			}
		}
	}
	return text.String(), nil
}

// run sends one command and returns its output.
func (s *shellConn) run(cmd string, idle func() error) (string, error) {
	s.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := s.c.Write([]byte(cmd + "\r\n")); err != nil {
		return "", err
	}
	return s.untilPrompt(0, idle)
}

// interrupt kills the running command (telnet "interrupt process").
func (s *shellConn) interrupt() {
	s.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.c.Write([]byte{255, 244})
	s.untilPrompt(8*time.Second, nil)
}

// shellSafe reports whether a path can be passed to the shell as one word.
func shellSafe(p string) bool {
	return p != "" && !strings.ContainsAny(p, " \t\r\n\a|;&")
}

func (f *ftpConn) rename(from, to string) error {
	code, msg, err := f.cmd("RNFR %s", from)
	if err != nil {
		return err
	}
	if code != 350 {
		return fmt.Errorf("the PS5 couldn't find %s (%s)", path.Base(from), strings.TrimSpace(msg))
	}
	code, msg, err = f.cmd("RNTO %s", to)
	if err != nil {
		return err
	}
	if !ok2xx(code) {
		return fmt.Errorf("the PS5 wouldn't rename %s (%s)", path.Base(from), strings.TrimSpace(msg))
	}
	return nil
}

// startPS5Move queues moves of exFAT images to another PS5 drive.
func (a *App) startPS5Move(paths []string, destRoot string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.ps5info.Online {
		return errors.New("The PS5 isn't reachable right now.")
	}
	if !a.ps5info.Shell {
		return errors.New("Moving games needs the shsrv payload running on the PS5. Send it from Settings › Payloads, then try again.")
	}
	destRoot = rootOf(destRoot)
	var dest *PS5Drive
	for i := range a.ps5info.Drives {
		if a.ps5info.Drives[i].Root == destRoot {
			dest = &a.ps5info.Drives[i]
		}
	}
	if dest == nil {
		return errors.New("That drive isn't connected to the PS5 any more.")
	}
	destDir := a.dirForLocked(destRoot)
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	var jobs []*BuildJob
	var need int64
	skippedKind, skippedSame := 0, 0
	for id, cs := range a.ps5info.Games {
		for _, c := range cs {
			if !want[c.Path] {
				continue
			}
			delete(want, c.Path)
			if c.Kind != "image" {
				skippedKind++
				continue
			}
			if rootOf(c.Path) == destRoot {
				skippedSame++
				continue
			}
			name := id
			for _, e := range a.lib.Games {
				if e.TitleID == id && e.Name != "" {
					name = e.Name
					break
				}
			}
			need += c.Size
			jobs = append(jobs, &BuildJob{Key: "ps5:" + c.Path, Kind: "ps5move", Name: name, Out: c.Drive + " → " + dest.Short,
				State: "queued", Size: c.Size, image: c.Path, Dest: destDir, DestLabel: dest.Short})
		}
	}
	if len(jobs) == 0 {
		switch {
		case skippedSame > 0:
			return errors.New("That game is already on that drive.")
		case skippedKind > 0:
			return errors.New("Only exFAT images can be moved. Game folders and installed games can't.")
		}
		return errors.New("Those games aren't on the PS5 any more. Click Rescan PS5.")
	}
	if dest.Free >= 0 && need > dest.Free+dest.Slack {
		return fmt.Errorf("Not enough room on %s: it has %s free and this needs %s.", dest.Label, humanSize(dest.Free), humanSize(need))
	}
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	return nil
}

// ps5Move runs one move job.
func (a *App) ps5Move(j *BuildJob) (string, error) {
	host, port, _ := a.ps5()
	src, destDir := j.image, j.Dest
	srcDir, base := path.Dir(src), path.Base(src)
	if !shellSafe(srcDir) || !shellSafe(destDir) {
		return "", errors.New("one of the folders has a space or symbol in its name, which the PS5's shell can't handle")
	}
	cancelled := func() bool { return a.ps5Stopped(j) }
	a.mu.Lock()
	j.State, j.Pct, j.Note = "moving", 0, "Getting ready"
	a.build.Sending, a.build.SendFile = j.Name, base
	a.mu.Unlock()

	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	size, ok := f.size(src)
	if !ok {
		f.Close()
		return "", errors.New("it isn't on the PS5 any more")
	}
	if free, slack := freeOn(host, destDir); free >= 0 && size > free+slack {
		f.Close()
		return "", fmt.Errorf("not enough room on %s: %s free, this needs %s", j.DestLabel, humanSize(free), humanSize(size))
	}
	if err := f.mkdirAll(destDir); err != nil {
		f.Close()
		return "", err
	}
	if _, exists := f.size(path.Join(destDir, base)); exists {
		f.Close()
		return "", fmt.Errorf("%s already has a file with this name", j.DestLabel)
	}
	tmp := fmt.Sprintf("ttmove-%s-%d.tmp", gameID(base), time.Now().Unix()%100000)
	srcTmp, dstTmp := path.Join(srcDir, tmp), path.Join(destDir, tmp+".part")
	if err := f.rename(src, srcTmp); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	// from here on, always put the original's name back if we don't finish
	restore := func() {
		for try := 0; try < 3; try++ {
			if g, err := ftpDial(host, port); err == nil {
				g.cmd("DELE %s", dstTmp)
				rerr := g.rename(srcTmp, src)
				g.Close()
				if rerr == nil {
					return
				}
			}
			time.Sleep(time.Second)
		}
	}

	sh, err := shellOpen(host)
	if err != nil {
		restore()
		return "", err
	}
	start := time.Now()
	var lastSize int64
	lastGrow := time.Now()
	var poll *ftpConn
	defer func() {
		if poll != nil {
			poll.Close()
		}
	}()
	out, err := sh.run("cp "+srcTmp+" "+dstTmp, func() error {
		if cancelled() {
			return errFTPCancelled
		}
		if poll == nil {
			poll, _ = ftpDial(host, port)
		}
		if poll != nil {
			got, ok := poll.size(dstTmp)
			if !ok {
				poll.Close()
				poll = nil
			} else {
				if got > lastSize {
					lastSize, lastGrow = got, time.Now()
				}
				speed := float64(got) / time.Since(start).Seconds()
				a.mu.Lock()
				j.Note = ""
				if size > 0 {
					j.Pct = int(got * 1000 / size)
				}
				a.build.SendSpeed = int64(speed)
				if speed > 0 {
					a.build.SendETA = int64(float64(size-got) / speed)
				}
				a.mu.Unlock()
			}
		}
		if time.Since(lastGrow) > 4*time.Minute {
			return errors.New("the copy stopped making progress")
		}
		return nil
	})
	if err != nil {
		sh.interrupt() // stop the copy on the PS5
	}
	sh.Close()
	if poll != nil {
		poll.Close()
		poll = nil
	}
	fail := func(e error) (string, error) {
		restore()
		return "", e
	}
	if err != nil {
		if errors.Is(err, errFTPCancelled) {
			restore()
			return "Stopped. The game is still on its original drive.", err
		}
		return fail(fmt.Errorf("the copy on the PS5 failed: %v", err))
	}
	if i := strings.Index(out, "cp:"); i >= 0 {
		msg := strings.TrimSpace(strings.SplitN(out[i+3:], "\n", 2)[0])
		return fail(fmt.Errorf("the PS5 couldn't copy it (%s)", msg))
	}
	g, err := ftpDial(host, port)
	if err != nil {
		return fail(err)
	}
	defer g.Close()
	if got, ok := g.size(dstTmp); !ok || got != size {
		g.Close()
		return fail(fmt.Errorf("the copy came out the wrong size (%s of %s), so the original was kept", humanSize(got), humanSize(size)))
	}
	if err := g.rename(dstTmp, path.Join(destDir, base)); err != nil {
		g.Close()
		return fail(err)
	}
	// the copy is complete and named: now the original can go
	if code, _, err := g.cmd("DELE %s", srcTmp); err != nil || !ok2xx(code) {
		g.rename(srcTmp, src)
		return fmt.Sprintf("Copied to %s in %s, but the original couldn't be removed (is the game running?). It's on both drives now.", j.DestLabel, fmtDur(time.Since(start))), nil
	}
	return fmt.Sprintf("Moved to %s in %s", j.DestLabel, fmtDur(time.Since(start))), nil
}
