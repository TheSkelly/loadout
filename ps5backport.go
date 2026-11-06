package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Backporting a game that's already on the PS5 as a folder. Only its
// programs move: eboot.bin and the .prx/.sprx at the top and in sce_module
// come over FTP to the PC, are backported there (backporter, the same steps
// as for a game on the PC) and go back in place. The originals are kept on
// the PC in backport-backups\ps5. A fakelib folder is added from Settings
// when the game has none. Close the game on the PS5 first: ShadowMountPlus
// can't remount a folder that's running.

// ps5Programs lists a PS5 game folder's programs, and whether it has fakelib files.
func ps5Programs(f *ftpConn, dir string) (progs []ftpEntry, rels []string, fakelib int, err error) {
	ents, err := f.list(dir)
	if err != nil {
		return nil, nil, 0, err
	}
	hasEboot := false
	for _, e := range ents {
		switch {
		case e.IsDir && strings.EqualFold(e.Name, "fakelib"):
			if in, err := f.list(path.Join(dir, e.Name)); err == nil {
				for _, x := range in {
					if !x.IsDir && x.Name != "." && x.Name != ".." {
						fakelib++
					}
				}
			}
		case e.IsDir && strings.EqualFold(e.Name, "sce_module"):
			if in, err := f.list(path.Join(dir, e.Name)); err == nil {
				for _, x := range in {
					if !x.IsDir && isProgram(x.Name) {
						progs = append(progs, x)
						rels = append(rels, e.Name+"/"+x.Name)
					}
				}
			}
		case !e.IsDir && isProgram(e.Name):
			if strings.EqualFold(e.Name, "eboot.bin") {
				hasEboot = true
			}
			progs = append(progs, e)
			rels = append(rels, e.Name)
		}
	}
	if !hasEboot {
		return nil, nil, 0, errors.New("that folder has no eboot.bin at its top, so it isn't a game folder")
	}
	return progs, rels, fakelib, nil
}

// ps5BackportCheck reads what a game folder on the PS5 asks for.
func (a *App) ps5BackportCheck(dir string) (*backportCheck, error) {
	a.mu.Lock()
	host, port, fw := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port, a.cfg.ConsoleFW
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return nil, errors.New("Loadout can't reach the PS5's FTP server.")
	}
	defer f.Close()
	progs, rels, fake, err := ps5Programs(f, dir)
	if err != nil {
		return nil, err
	}
	c := &backportCheck{Fakelib: fake}
	for i, e := range progs {
		// the parameters usually sit near the start of a program; read it all when they don't
		remote := path.Join(dir, rels[i])
		data, err := f.get(remote, 4<<20)
		if err == nil && e.Size > int64(len(data)) {
			if _, _, perr := programSDK(bytesReaderAt(data), int64(len(data))); perr != nil {
				local := filepath.Join(os.TempDir(), fmt.Sprintf("loadout-ps5check-%d", os.Getpid()))
				err = f.download(remote, local, func(int64) {}, nil)
				if err == nil {
					data, err = os.ReadFile(local)
				}
				os.Remove(local)
			}
		}
		if err != nil {
			c.Programs = append(c.Programs, progSDK{Name: rels[i], Kind: "unreadable"})
			c.Unreadable++
			continue
		}
		c.add(rels[i], bytesReaderAt(data), int64(len(data)))
	}
	if b, err := f.get(path.Join(dir, "sce_sys", "param.json"), 1<<20); err == nil {
		c.ReqFW = paramFirmware(b)
	}
	c.finish()
	c.judge(fw)
	return c, nil
}

// ps5Folders are the game-folder copies of a game on the PS5.
func ps5Folders(cs []PS5Copy) []string {
	out := []string{}
	for _, c := range cs {
		if c.Kind == "folder" {
			out = append(out, c.Path)
		}
	}
	return out
}

// deletableCopies are the copies of a game Loadout can delete from the PS5:
// images and game folders. Installed games go from the PS5's own menu.
func deletableCopies(cs []PS5Copy) []PS5Copy {
	out := []PS5Copy{}
	for _, c := range cs {
		if c.Kind == "image" || c.Kind == "folder" {
			out = append(out, c)
		}
	}
	return out
}

// startPS5Backport queues a backport of a game folder on the PS5.
func (a *App) startPS5Backport(dir string) error {
	dir = path.Clean("/" + strings.ReplaceAll(dir, "\\", "/"))
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	if strings.TrimSpace(a.cfg.ConsoleFW) == "" {
		return errors.New("Set your PS5's firmware in Settings first, so Loadout knows what to backport to.")
	}
	for _, j := range a.build.Jobs {
		if j.Kind == "ps5backport" && j.Dest == dir && (j.State == "queued" || j.State == "sending") {
			return errors.New("That game is already being backported.")
		}
	}
	j := &BuildJob{Key: "ps5bp:" + dir, Kind: "ps5backport", Name: path.Base(dir), Out: path.Base(dir), State: "queued", Dest: dir}
	a.addJobsLocked([]*BuildJob{j})
	a.enqueuePS5Locked(j)
	return nil
}

// runPS5Backport backports a game folder on the PS5 in place.
func (a *App) runPS5Backport(j *BuildJob) (string, error) {
	a.mu.Lock()
	host, port, fw, fakeSrc := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port, a.cfg.ConsoleFW, a.cfg.FakelibDir
	a.mu.Unlock()
	note := func(s string, pct int) {
		a.mu.Lock()
		j.State, j.Note, j.Pct = "sending", s, pct
		a.mu.Unlock()
	}
	bp, err := newBackporter(fw)
	if err != nil {
		return "", err
	}
	defer bp.close()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", errors.New("Loadout can't reach the PS5's FTP server")
	}
	defer f.Close()
	dir := j.Dest
	progs, rels, fake, err := ps5Programs(f, dir)
	if err != nil {
		return "", err
	}
	backups := filepath.Join(appDir(), "backport-backups", "ps5", path.Base(dir))
	patched := 0
	for i, e := range progs {
		if a.ps5Stopped(j) {
			return "", ErrCancelled
		}
		rel, remote := rels[i], path.Join(dir, rels[i])
		note("Reading "+rel+" from the PS5", i*1000/len(progs))
		local := filepath.Join(bp.tmp, "ps5-program")
		os.Remove(local)
		if err := f.download(remote, local, func(int64) {}, func() bool { return a.ps5Stopped(j) }); err != nil {
			return "", fmt.Errorf("couldn't read %s from the PS5: %v", rel, err)
		}
		data, err := os.ReadFile(local)
		if err != nil || int64(len(data)) != e.Size {
			return "", fmt.Errorf("%s didn't come over whole from the PS5", rel)
		}
		note("Backporting "+rel, i*1000/len(progs)+300/len(progs))
		signed, err := bp.program(rel, data)
		if err != nil {
			return "", err
		}
		if signed == nil {
			continue
		}
		// keep the original on the PC before anything changes on the PS5
		bk := filepath.Join(backups, filepath.FromSlash(rel))
		if !fileExists(bk) {
			os.MkdirAll(filepath.Dir(bk), 0755)
			if err := os.WriteFile(bk, data, 0644); err != nil {
				return "", fmt.Errorf("couldn't keep a copy of %s: %v", rel, err)
			}
		}
		note("Putting "+rel+" back on the PS5", i*1000/len(progs)+600/len(progs))
		tmp := remote + ".loadout-new"
		signedLocal := filepath.Join(bp.tmp, "ps5-signed")
		if err := os.WriteFile(signedLocal, signed, 0644); err != nil {
			return "", err
		}
		if _, err := f.uploadAs(signedLocal, path.Dir(remote), path.Base(tmp), true, func(int64, int64) {}, func() bool { return a.ps5Stopped(j) }); err != nil {
			f.cmd("DELE %s", tmp)
			return "", fmt.Errorf("couldn't copy %s to the PS5: %v", rel, err)
		}
		if n, ok := f.size(tmp); !ok || n != int64(len(signed)) {
			f.cmd("DELE %s", tmp)
			return "", fmt.Errorf("%s didn't arrive whole on the PS5", rel)
		}
		if code, msg, err := f.cmd("DELE %s", remote); err != nil || !ok2xx(code) {
			f.cmd("DELE %s", tmp)
			return "", fmt.Errorf("the PS5 wouldn't replace %s (%s). Is the game running?", rel, strings.TrimSpace(msg))
		}
		if err := f.rename(tmp, remote); err != nil {
			// put the original back rather than leave the game without it
			f.uploadAs(local, path.Dir(remote), path.Base(remote), true, func(int64, int64) {}, func() bool { return false })
			f.cmd("DELE %s", tmp)
			return "", fmt.Errorf("couldn't put %s in place: %v", rel, err)
		}
		patched++
	}
	// the firmware param.json asks for
	paramNote := ""
	pj := path.Join(dir, "sce_sys", "param.json")
	if b, err := f.get(pj, 1<<20); err == nil && len(b) > 0 {
		if nb, old, changed := lowerParamFirmware(b, fw); changed {
			note("Changing param.json", 900)
			bk := filepath.Join(backups, "sce_sys", "param.json")
			if !fileExists(bk) {
				os.MkdirAll(filepath.Dir(bk), 0755)
				os.WriteFile(bk, b, 0644)
			}
			if err := f.put(pj, nb); err != nil {
				return "", fmt.Errorf("couldn't change param.json on the PS5: %v", err)
			}
			if again, err := f.get(pj, 1<<20); err != nil || string(again) != string(nb) {
				f.put(pj, b) // put it back as it was
				return "", errors.New("param.json didn't save right on the PS5, so it was put back")
			}
			paramNote = fmt.Sprintf("param.json now asks for %s instead of %s", fw, old)
		}
	}
	added := 0
	if fake == 0 && fakeSrc != "" {
		ents, err := os.ReadDir(fakeSrc)
		if err != nil {
			return "", fmt.Errorf("the fakelib folder picked in Settings can't be read: %v", err)
		}
		fdir := path.Join(dir, "fakelib")
		if err := f.mkdirAll(fdir); err != nil {
			return "", err
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			note("Adding fakelib "+e.Name(), 950)
			b, err := os.ReadFile(filepath.Join(fakeSrc, e.Name()))
			if err == nil && f.put(path.Join(fdir, e.Name()), b) == nil {
				added++
			}
		}
		fake = added
	}
	msg := fmt.Sprintf("Backported on the PS5 for %s: %d program%s lowered to SDK %s", fw, patched, plural(patched), sdkText(bp.ps5))
	if patched == 0 {
		msg = fmt.Sprintf("Its programs already ask for %s or older", fw)
	}
	if paramNote != "" {
		msg += "; " + paramNote
	}
	switch {
	case added > 0:
		msg += fmt.Sprintf(", %d fakelib file%s added", added, plural(added))
	case fake > 0:
		msg += ", its own fakelib folder kept"
	default:
		msg += ". It has no fakelib folder: if it still won't start, pick a fakelib folder in Settings and backport it again"
	}
	if patched > 0 {
		msg += ". The originals are kept on the PC in " + backups
	}
	return msg, nil
}
