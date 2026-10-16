package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// The backport job: a game folder in place, or a game that's only an exFAT
// image after turning it into a folder (the image is kept).

// makeFselfScript finds tools\make_fself.py beside Loadout (or in the source tree).
func makeFselfScript() string {
	exe, _ := os.Executable()
	for _, p := range []string{filepath.Join(filepath.Dir(exe), "tools", "make_fself.py"), filepath.Join("tools", "make_fself.py")} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// pythonCmd is Python 3 on this PC: python, or the py launcher.
func pythonCmd() ([]string, error) {
	for _, c := range [][]string{{"python"}, {"py", "-3"}, {"python3"}} {
		p, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(p, append(c[1:], "-c", "import sys; print(sys.version_info[0])")...)
		hideChild(cmd)
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) == "3" {
			return append([]string{p}, c[1:]...), nil
		}
	}
	return nil, errors.New("backports need Python 3 on this PC (from python.org): it signs the patched programs")
}

// backportPrograms are the programs a backport patches: the top of the game
// and sce_module (fakelib and leftovers from other tools aside).
func backportPrograms(root string) []string {
	var out []string
	for _, dir := range []string{root, filepath.Join(root, "sce_module")} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() && isProgram(e.Name()) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

// runBackport backports the game folder at root for the console's firmware.
// Every program that asks for a newer SDK is turned back into an ELF,
// lowered, signed again and checked; the originals are kept in Loadout's
// backport-backups folder. The game's own fakelib folder is kept, or one is
// copied from the fakelib folder picked in Settings.
func (a *App) runBackport(j *BuildJob, root string, cancelled func() bool) (string, error) {
	a.mu.Lock()
	fw, fakeSrc := a.cfg.ConsoleFW, a.cfg.FakelibDir
	a.mu.Unlock()
	bp, err := newBackporter(fw)
	if err != nil {
		return "", err
	}
	defer bp.close()
	if !fileExists(filepath.Join(root, "eboot.bin")) {
		return "", errors.New("there's no eboot.bin at the top of the game folder")
	}
	note := func(s string, pct int) {
		a.mu.Lock()
		j.State, j.Note, j.Pct = "building", s, pct
		a.mu.Unlock()
	}
	progs := backportPrograms(root)
	backups := filepath.Join(appDir(), "backport-backups", filepath.Base(root))
	patched := 0
	for i, p := range progs {
		if cancelled() {
			return "", ErrCancelled
		}
		rel, _ := filepath.Rel(root, p)
		note("Backporting "+rel, i*1000/len(progs))
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		signed, err := bp.program(rel, data)
		if err != nil {
			return "", err
		}
		if signed == nil {
			continue // already old enough, or not a program with an SDK
		}
		// keep the original, then put the backported one in its place
		bk := filepath.Join(backups, rel)
		if !fileExists(bk) {
			os.MkdirAll(filepath.Dir(bk), 0755)
			if err := os.WriteFile(bk, data, 0644); err != nil {
				return "", fmt.Errorf("couldn't keep a copy of %s: %v", rel, err)
			}
		}
		if err := os.WriteFile(p+".loadout-new", signed, 0644); err != nil {
			return "", err
		}
		if err := os.Rename(p+".loadout-new", p); err != nil {
			os.Remove(p + ".loadout-new")
			return "", err
		}
		patched++
	}
	// the firmware param.json asks for: the PS5 won't start a game that says
	// it needs newer firmware, even one built for this one
	paramNote := ""
	pj := filepath.Join(root, "sce_sys", "param.json")
	if b, err := os.ReadFile(pj); err == nil {
		if nb, old, changed := lowerParamFirmware(b, fw); changed {
			bk := filepath.Join(backups, "sce_sys", "param.json")
			if !fileExists(bk) {
				os.MkdirAll(filepath.Dir(bk), 0755)
				os.WriteFile(bk, b, 0644)
			}
			if err := os.WriteFile(pj, nb, 0644); err != nil {
				return "", fmt.Errorf("couldn't change param.json: %v", err)
			}
			paramNote = fmt.Sprintf("param.json now asks for %s instead of %s", fw, old)
		}
	}
	// the fakelib folder: the game's own, or the one picked in Settings
	fake := filepath.Join(root, "fakelib")
	have := 0
	if ents, err := os.ReadDir(fake); err == nil {
		have = len(ents)
	}
	added := 0
	if have == 0 && fakeSrc != "" {
		ents, err := os.ReadDir(fakeSrc)
		if err != nil {
			return "", fmt.Errorf("the fakelib folder picked in Settings can't be read: %v", err)
		}
		os.MkdirAll(fake, 0755)
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(fakeSrc, e.Name()))
			if err == nil && os.WriteFile(filepath.Join(fake, e.Name()), b, 0644) == nil {
				added++
			}
		}
		have = added
	}
	msg := fmt.Sprintf("Backported for %s: %d program%s lowered to SDK %s", fw, patched, plural(patched), sdkText(bp.ps5))
	if patched == 0 {
		msg = fmt.Sprintf("Every program already asks for %s or older", fw)
	}
	if paramNote != "" {
		msg += "; " + paramNote
	}
	switch {
	case added > 0:
		msg += fmt.Sprintf(", %d fakelib file%s added", added, plural(added))
	case have > 0:
		msg += ", its own fakelib folder kept"
	default:
		msg += ". It has no fakelib folder: if it still won't start, pick a fakelib folder in Settings and backport again"
	}
	if patched > 0 {
		msg += ". The original programs are kept in " + backups
	}
	return msg, nil
}

// bytesReaderAt reads from memory.
type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// startBackport backports games: a game folder in place, a game that's only
// an image after turning it into a folder. send queues each finished folder
// for the PS5.
// backportItem is a game to backport: its library key, and its image for a
// game that has no folder.
type backportItem struct{ Key, Image string }

func (a *App) startBackport(items []backportItem, send bool, drive string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.build.Building {
		return errors.New("Wait for the build to finish first.")
	}
	if strings.TrimSpace(a.cfg.ConsoleFW) == "" {
		return errors.New("Set your PS5's firmware in Settings first, so Loadout knows what to backport to.")
	}
	if send && strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	var jobs []*BuildJob
	for _, it := range items {
		k := it.Key
		if g := a.gameByKey(k); g != nil && !g.IsFile && g.Meta.Root != "" && fileExists(filepath.Join(g.Meta.Root, "eboot.bin")) {
			name := g.Name
			if name == "" {
				name = g.Current
			}
			jobs = append(jobs, &BuildJob{Key: k, Kind: "backport", Name: name, Out: folderNameOnPS5(g), State: "queued",
				image: g.Meta.Root, sendAfter: send, sendDrive: drive})
			continue
		}
		img := it.Image
		fi, err := os.Stat(img)
		if img == "" || err != nil || !strings.EqualFold(filepath.Ext(img), ".exfat") {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(img), filepath.Ext(img))
		jobs = append(jobs, &BuildJob{Key: "img:" + img, Kind: "unpack", Name: name, Out: name, State: "queued", Size: fi.Size(),
			image: img, outDir: a.gamesFolderForImageLocked(img), sendAfter: send, sendDrive: drive, backportAfter: true})
	}
	if len(jobs) == 0 {
		return errors.New("There's no game folder or image there to backport.")
	}
	atomic.StoreInt32(&a.cancel, 0)
	a.addJobsLocked(jobs)
	a.build.Building = true
	go a.runBuild(jobs, a.outDir(), false, false)
	return nil
}

// queueFolderSendLocked sends a finished game folder to the PS5.
func (a *App) queueFolderSendLocked(j *BuildJob, folder string) {
	sj := &BuildJob{Key: "folder:" + folder, Kind: "sendfolder", Name: j.Name, Out: j.Out, State: "queued", Size: folderSize(folder), image: folder}
	sj.Dest, sj.DestLabel = a.destLocked(j.sendDrive)
	a.addJobsLocked([]*BuildJob{sj})
	a.enqueuePS5Locked(sj)
}

// backporter backports one program at a time for a console firmware.
type backporter struct {
	ps5, ps4, limit uint32
	py              []string
	script, tmp     string
}

func newBackporter(fw string) (*backporter, error) {
	ps5, ps4, err := backportTargets(fw)
	if err != nil {
		return nil, err
	}
	py, err := pythonCmd()
	if err != nil {
		return nil, err
	}
	script := makeFselfScript()
	if script == "" {
		return nil, errors.New("make_fself.py isn't with this copy of Loadout")
	}
	tmp, err := os.MkdirTemp("", "loadout-backport-")
	if err != nil {
		return nil, err
	}
	return &backporter{ps5: ps5, ps4: ps4, limit: ps5 | 0xFFFF, py: py, script: script, tmp: tmp}, nil
}

func (b *backporter) close() { os.RemoveAll(b.tmp) }

// needs: the program asks for a newer SDK than the console runs.
func (b *backporter) needs(data []byte) bool {
	v, _, err := programSDK(bytesReaderAt(data), int64(len(data)))
	return err == nil && v > b.limit
}

// program answers the backported program, or nil when it doesn't need it.
func (b *backporter) program(rel string, data []byte) ([]byte, error) {
	if !b.needs(data) {
		return nil, nil
	}
	elf := data
	if string(data[:4]) != string(elfMagic) {
		var err error
		if elf, err = fselfToELF(data); err != nil {
			return nil, fmt.Errorf("%s: %v", rel, err)
		}
	} else {
		elf = append([]byte(nil), data...)
	}
	if _, err := patchELFSDK(elf, b.ps5, b.ps4); err != nil {
		return nil, fmt.Errorf("%s: %v", rel, err)
	}
	in, out := filepath.Join(b.tmp, "in.elf"), filepath.Join(b.tmp, "out.self")
	os.Remove(out)
	if err := os.WriteFile(in, elf, 0644); err != nil {
		return nil, err
	}
	cmd := exec.Command(b.py[0], append(b.py[1:], "-I", b.script, in, out)...)
	hideChild(cmd)
	if o, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("signing %s failed: %v %s", rel, err, strings.TrimSpace(string(o)))
	}
	signed, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if nv, _, err := programSDK(bytesReaderAt(signed), int64(len(signed))); err != nil || nv > b.limit {
		return nil, fmt.Errorf("%s didn't come out right after signing", rel)
	}
	return signed, nil
}
