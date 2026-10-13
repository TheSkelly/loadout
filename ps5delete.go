package main

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// removeAll deletes a file, or a whole folder tree, on the PS5.
func (f *ftpConn) removeAll(p string, isDir bool, onFile func(), cancelled func() bool) error {
	if !isDir {
		code, msg, err := f.cmd("DELE %s", p)
		if err != nil {
			return err
		}
		if !ok2xx(code) {
			return fmt.Errorf("the PS5 wouldn't delete %s (%s)", path.Base(p), strings.TrimSpace(msg))
		}
		onFile()
		return nil
	}
	ents, err := f.list(p)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		if cancelled() {
			return errFTPCancelled
		}
		if err := f.removeAll(path.Join(p, e.Name), e.IsDir, onFile, cancelled); err != nil {
			return err
		}
	}
	code, msg, err := f.cmd("RMD %s", p)
	if err != nil {
		return err
	}
	if !ok2xx(code) {
		return fmt.Errorf("the PS5 wouldn't remove the folder %s (%s)", path.Base(p), strings.TrimSpace(msg))
	}
	return nil
}

// startPS5Delete deletes game copies the last PS5 scan found. Only images and
// game folders can go; installed games are removed from the PS5's own menu.
func (a *App) startPS5Delete(paths []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.ps5info.Online {
		return errors.New("The PS5 isn't reachable right now.")
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	var jobs []*BuildJob
	for id, cs := range a.ps5info.Games {
		for _, c := range cs {
			if !want[c.Path] {
				continue
			}
			if c.Kind == "installed" {
				return errors.New("Installed games have to be deleted from the PS5's own game menu.")
			}
			name := id
			for _, e := range a.lib.Games {
				if e.TitleID == id && e.Name != "" {
					name = e.Name
					break
				}
			}
			jobs = append(jobs, &BuildJob{Key: "ps5:" + c.Path, Kind: "ps5del", Name: name, Out: c.Drive + ": " + c.Path,
				State: "queued", Size: c.Size, image: c.Path, remoteDir: c.Kind == "folder"})
			delete(want, c.Path)
		}
	}
	if len(jobs) == 0 {
		return errors.New("Those copies aren't on the PS5 any more. Click Refresh in Settings to rescan.")
	}
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	return nil
}

// ps5Delete runs one delete job.
func (a *App) ps5Delete(j *BuildJob) (string, error) {
	host, port, _ := a.ps5()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n := 0
	a.mu.Lock()
	j.State, j.Note = "deleting", "Deleting from the PS5"
	a.mu.Unlock()
	err = f.removeAll(j.image, j.remoteDir, func() {
		n++
		if j.remoteDir && n%25 == 0 {
			a.mu.Lock()
			j.Note = fmt.Sprintf("Deleting from the PS5, %d files so far", n)
			a.mu.Unlock()
		}
	}, func() bool { return a.ps5Stopped(j) })
	if err != nil {
		if errors.Is(err, errFTPCancelled) {
			return fmt.Sprintf("Stopped after deleting %d files. The rest is still on the PS5.", n), err
		}
		return "", err
	}
	if j.remoteDir {
		return fmt.Sprintf("Deleted from the PS5 (%d files)", n), nil
	}
	return "Deleted from the PS5", nil
}
