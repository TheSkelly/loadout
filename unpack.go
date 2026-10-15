package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Turning an exFAT image back into a game folder: the image's files are
// copied out, as they are, into a folder in the games folder the image
// belongs to (the one its "exFAT Images" folder is in), named like the
// image. It's written under a hidden name first and renamed once every file
// is out at its full size, so the library never sees half a game. The image
// is kept. Runs in the build lane, one image at a time.

// startUnpack queues images to turn into game folders.
func (a *App) startUnpack(images []string, send bool, drive string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.build.Building {
		return errors.New("Wait for the build to finish first.")
	}
	if send && strings.TrimSpace(a.cfg.PS5IP) == "" {
		return errors.New("Set your PS5's IP address in Settings first.")
	}
	var jobs []*BuildJob
	for _, img := range images {
		fi, err := os.Stat(img)
		if err != nil || !strings.EqualFold(filepath.Ext(img), ".exfat") {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(img), filepath.Ext(img))
		jobs = append(jobs, &BuildJob{Key: "img:" + img, Kind: "unpack", Name: name, Out: name, State: "queued", Size: fi.Size(),
			image: img, outDir: a.gamesFolderForImageLocked(img), sendAfter: send, sendDrive: drive})
	}
	if len(jobs) == 0 {
		return errors.New("There's no exFAT image there to turn into a folder.")
	}
	atomic.StoreInt32(&a.cancel, 0)
	a.addJobsLocked(jobs)
	a.build.Building = true
	go a.runBuild(jobs, a.outDir(), false, false)
	return nil
}

// gamesFolderForImageLocked: an image in a games folder's "exFAT Images"
// unpacks into that games folder; any other image into the main one.
func (a *App) gamesFolderForImageLocked(img string) string {
	dir := filepath.Dir(img)
	if strings.EqualFold(filepath.Base(dir), "exFAT Images") {
		return filepath.Dir(dir)
	}
	return a.cfg.Folder
}

// runUnpack turns one image into a game folder; it answers the job's note.
func (a *App) runUnpack(j *BuildJob, cancelled func() bool) (string, error) {
	final := filepath.Join(j.outDir, j.Out)
	if _, err := os.Stat(final); err == nil {
		return "", fmt.Errorf("%s already has a folder named %s", j.outDir, j.Out)
	}
	if free := diskFree(j.outDir); free >= 0 && free < j.Size+(256<<20) {
		return "", fmt.Errorf("not enough space in %s: needs %s, %s free", j.outDir, humanSize(j.Size), humanSize(free))
	}
	tmp := filepath.Join(j.outDir, ".loadout-unpacking-"+j.Out)
	os.RemoveAll(tmp)
	start := time.Now()
	var last time.Time
	err := unpackExfat(j.image, tmp, func(done, total int64) {
		if time.Since(last) < 150*time.Millisecond && done < total {
			return
		}
		last = time.Now()
		a.mu.Lock()
		if total > 0 {
			j.Pct = int(done * 1000 / total)
		}
		j.Note = ""
		if secs := time.Since(start).Seconds(); secs > 0 {
			speed := float64(done) / secs
			a.build.Speed = int64(speed)
			if speed > 0 {
				a.build.ETA = int64(float64(total-done) / speed)
			}
		}
		a.mu.Unlock()
	}, cancelled)
	if err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("couldn't name the folder: %v", err)
	}
	note := fmt.Sprintf("Turned into a folder in %s, in %s. The image is kept", j.outDir, fmtDur(time.Since(start)))
	if j.backportAfter {
		bn, err := a.runBackport(j, final, cancelled)
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				return "", err
			}
			return "", fmt.Errorf("turned into a folder (%s), but the backport didn't work: %v", final, err)
		}
		note = "Turned into a folder. " + bn
	}
	if j.sendAfter {
		// a folder game loads faster on the PS5 than an image: send it there now
		a.mu.Lock()
		a.queueFolderSendLocked(j, final)
		a.mu.Unlock()
		note += ". Sending the folder to the PS5"
	}
	return note, nil
}

// unpackExfat copies every file in an exFAT image into dst, keeping its folders.
func unpackExfat(image, dst string, progress func(done, total int64), cancelled func() bool) error {
	im, root, err := openExImage(image)
	if err != nil {
		return errors.New("that file isn't an exFAT image Loadout can read")
	}
	defer im.f.Close()
	// add up the files first, for the progress
	var total int64
	var count func(ents []exEntry) error
	count = func(ents []exEntry) error {
		for _, e := range ents {
			if verifySkip[strings.ToLower(e.name)] {
				continue
			}
			if e.isDir {
				in, err := im.listDir(e.first, e.length, e.noChain)
				if err != nil {
					return fmt.Errorf("%s: %v", e.name, err)
				}
				if err := count(in); err != nil {
					return err
				}
				continue
			}
			total += int64(e.length)
		}
		return nil
	}
	if err := count(root); err != nil {
		return err
	}
	var done int64
	buf := make([]byte, 4<<20)
	var copyOut func(ents []exEntry, dir string) error
	copyOut = func(ents []exEntry, dir string) error {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		for _, e := range ents {
			if verifySkip[strings.ToLower(e.name)] {
				continue
			}
			if strings.ContainsAny(e.name, `\/:*?"<>|`) || e.name == "." || e.name == ".." {
				return fmt.Errorf("the image has a name Windows can't use: %q", e.name)
			}
			p := filepath.Join(dir, e.name)
			if e.isDir {
				in, err := im.listDir(e.first, e.length, e.noChain)
				if err != nil {
					return fmt.Errorf("%s: %v", e.name, err)
				}
				if err := copyOut(in, p); err != nil {
					return err
				}
				continue
			}
			runs, err := im.runs(e.first, e.length, e.noChain)
			if err != nil {
				return fmt.Errorf("%s: %v", e.name, err)
			}
			f, err := os.Create(p)
			if err != nil {
				return err
			}
			left := int64(e.length)
			for _, r := range runs {
				off, n := im.heap+int64(r[0]-2)*im.cs, int64(r[1])*im.cs
				for n > 0 && left > 0 {
					if cancelled != nil && cancelled() {
						f.Close()
						return ErrCancelled
					}
					step := int64(len(buf))
					if step > n {
						step = n
					}
					if step > left {
						step = left
					}
					if _, err := im.f.ReadAt(buf[:step], off); err != nil {
						f.Close()
						return fmt.Errorf("couldn't read %s from the image: %v", e.name, err)
					}
					if _, err := f.Write(buf[:step]); err != nil {
						f.Close()
						return fmt.Errorf("couldn't write %s: %v", e.name, err)
					}
					off, n, left = off+step, n-step, left-step
					done += step
					progress(done, total)
				}
			}
			if err := f.Close(); err != nil {
				return err
			}
			if fi, err := os.Stat(p); err != nil || fi.Size() != int64(e.length) {
				return fmt.Errorf("%s came out the wrong size", e.name)
			}
		}
		return nil
	}
	if err := copyOut(root, dst); err != nil {
		return err
	}
	progress(total, total)
	return nil
}
