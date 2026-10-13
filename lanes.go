package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Loadout runs two independent lanes:
//   - the build lane makes exFAT images (and recycles folders), one at a time
//   - the PS5 lane sends images and deletes from the PS5, one at a time
// Either can be started while the other is busy; both share one job list.

type ps5Stats struct {
	sent, failed, deleted, moved int
	saved, restored, savesGone   int
	copied                       int
	stopped                      bool
	start                        time.Time
}

// addJobsLocked adds jobs to the shared list, starting fresh when nothing is running.
func (a *App) addJobsLocked(jobs []*BuildJob) {
	if !a.build.Building && !a.build.PS5Busy {
		a.build = BuildState{}
		a.laneNotes = nil
	}
	a.build.Jobs = append(a.build.Jobs, jobs...)
	a.build.Running = true
	a.build.Summary = ""
}

// enqueuePS5Locked hands a job to the PS5 lane.
func (a *App) enqueuePS5Locked(j *BuildJob) {
	if !a.build.PS5Busy {
		a.build.PS5Busy, a.build.Running = true, true
		a.ps5st = ps5Stats{start: time.Now()}
	}
	j.gen = atomic.LoadInt32(&a.ps5stop)
	a.ps5q = append(a.ps5q, j)
	a.ps5cond.Broadcast()
}

type sendProg struct {
	left  int64
	speed float64
}

func (a *App) sendAtOnceLocked() int {
	if a.cfg.SendAtOnce >= 1 && a.cfg.SendAtOnce <= 4 {
		return a.cfg.SendAtOnce
	}
	return 2
}

// sumSendLocked adds up the live uploads for the speed and time-left readout.
func (a *App) sumSendLocked() {
	var left int64
	var speed float64
	for _, p := range a.sendProg {
		left += p.left
		speed += p.speed
	}
	a.build.SendSpeed, a.build.SendETA = int64(speed), 0
	if len(a.sendProg) == 0 {
		a.build.SendSlow = ""
	}
	if speed > 0 {
		a.build.SendETA = int64(float64(left) / speed)
	}
}

// ps5Worker runs PS5 jobs forever. Several of these run side by side.
func (a *App) ps5Worker() {
	for {
		a.mu.Lock()
		// sends run side by side; a delete or a move runs alone
		for {
			if len(a.ps5q) > 0 && !a.ps5solo {
				solo := a.ps5q[0].Kind == "ps5del" || a.ps5q[0].Kind == "ps5move"
				if (solo && a.ps5active == 0) || (!solo && a.ps5active < a.sendAtOnceLocked()) {
					a.ps5solo = solo
					break
				}
			}
			a.ps5cond.Wait()
		}
		j := a.ps5q[0]
		a.ps5q = a.ps5q[1:]
		a.ps5active++
		a.mu.Unlock()

		a.runPS5Job(j)

		a.mu.Lock()
		a.ps5active--
		a.ps5solo = false
		a.ps5cond.Broadcast()
		if len(a.ps5q) == 0 && a.ps5active == 0 && a.dropActive == 0 {
			a.build.PS5Busy = false
			a.build.Sending, a.build.SendSpeed, a.build.SendETA, a.build.SendFile = "", 0, 0, ""
			a.build.SendSlow = ""
			st := a.ps5st
			var parts []string
			if st.sent > 0 {
				parts = append(parts, fmt.Sprintf("Sent %d to the PS5 in %s.", st.sent, fmtDur(time.Since(st.start))))
			}
			if st.deleted > 0 {
				parts = append(parts, fmt.Sprintf("Deleted %d from the PS5.", st.deleted))
			}
			if st.moved > 0 {
				parts = append(parts, fmt.Sprintf("Moved %d on the PS5.", st.moved))
			}
			if st.saved > 0 {
				parts = append(parts, fmt.Sprintf("Backed up %d save(s).", st.saved))
			}
			if st.copied > 0 {
				parts = append(parts, fmt.Sprintf("Copied %d file(s).", st.copied))
			}
			if st.savesGone > 0 {
				parts = append(parts, fmt.Sprintf("Deleted %d save(s) from the PS5.", st.savesGone))
			}
			if st.restored > 0 {
				parts = append(parts, fmt.Sprintf("Restored %d save(s).", st.restored))
			}
			if st.failed > 0 {
				parts = append(parts, fmt.Sprintf("%d PS5 job(s) failed.", st.failed))
			}
			if st.stopped {
				parts = append([]string{"PS5 jobs stopped."}, parts...)
			}
			a.finishLaneLocked(strings.Join(parts, " "))
			if st.deleted > 0 || st.moved > 0 || st.failed > 0 {
				go func() {
					a.mu.Lock()
					a.ps5info.Busy = false
					a.mu.Unlock()
					a.refreshPS5()
				}()
			}
		}
		a.mu.Unlock()
	}
}

func (a *App) runPS5Job(j *BuildJob) {
	stopped := a.ps5Stopped(j)
	setState := func(state, note string) {
		a.mu.Lock()
		if j.Kind == "build" {
			j.SendState, j.SendNote = state, note
			if state == "sent" {
				j.SendPct = 1000
			}
		} else {
			j.State, j.Note = state, note
		}
		a.mu.Unlock()
	}
	ok := map[bool]string{true: "sent", false: "done"}[j.Kind == "build"]
	if stopped {
		setState("cancelled", "")
		a.mu.Lock()
		a.ps5st.stopped = true
		a.mu.Unlock()
		return
	}
	var note string
	var err error
	// the job runs on its own so that Stop always frees the lane: if a job is
	// still tangled up with the PS5 a few seconds after Stop, it is left to
	// wind down by itself and the lane moves on
	type result struct {
		note string
		err  error
	}
	resc := make(chan result, 1)
	go func() {
		var note string
		var err error
		switch j.Kind {
		case "ps5del":
			note, err = a.ps5Delete(j)
		case "ps5move":
			note, err = a.ps5Move(j)
		case "savebak":
			note, err = a.saveBackup(j)
		case "saverest":
			note, err = a.saveRestore(j)
		case "savedel":
			note, err = a.saveDelete(j)
		case "pkginst":
			note, err = a.pkgInstall(j)
		case "fxup", "fxdown", "fxupdir", "fxzip", "fxzipflat":
			note, err = a.fxCopy(j)
		case "build":
			note, err = a.sendImage(j, j.output)
		default: // "send"
			note, err = a.sendImage(j, j.image)
		}
		resc <- result{note, err}
	}()
	tick := time.NewTicker(300 * time.Millisecond)
	var stopAt time.Time
wait:
	for {
		select {
		case r := <-resc:
			note, err = r.note, r.err
			break wait
		case <-tick.C:
			if !a.ps5Stopped(j) {
				continue
			}
			if stopAt.IsZero() {
				stopAt = time.Now()
			} else if time.Since(stopAt) > 6*time.Second {
				note, err = "Stopped", errFTPCancelled
				break wait
			}
		}
	}
	tick.Stop()
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case errors.Is(err, ErrCancelled) || errors.Is(err, errFTPCancelled) || (err != nil && a.ps5Stopped(j)):
		a.ps5st.stopped = true
		if j.Kind == "build" {
			j.SendState, j.SendNote = "cancelled", note
		} else {
			j.State, j.Note = "cancelled", note
		}
	case err != nil:
		if j.Kind == "build" {
			j.SendState, j.SendNote = "failed", err.Error()
		} else {
			j.State, j.Note = "failed", err.Error()
		}
		a.ps5st.failed++
	default:
		if j.Kind == "build" {
			j.SendState, j.SendPct, j.SendNote = ok, 1000, note
		} else {
			j.State, j.Pct, j.Note = ok, 1000, note
		}
		switch j.Kind {
		case "ps5del":
			a.ps5st.deleted++
		case "ps5move":
			a.ps5st.moved++
		case "savebak":
			a.ps5st.saved++
		case "saverest":
			a.ps5st.restored++
		case "savedel":
			a.ps5st.savesGone++
		case "fxup", "fxdown", "fxupdir", "fxzip", "fxzipflat":
			a.ps5st.copied++
		default:
			a.ps5st.sent++
		}
	}
}

// finishLaneLocked records a lane's summary; when both lanes are idle the run is over.
func (a *App) finishLaneLocked(note string) {
	if note != "" {
		a.laneNotes = append(a.laneNotes, note)
	}
	if a.build.Building || a.build.PS5Busy {
		return
	}
	a.build.Running = false
	a.build.Summary = strings.Join(a.laneNotes, " ")
	a.laneNotes = nil
	if a.build.Summary != "" {
		a.notify("%s", a.build.Summary)
		a.alertRunLocked(a.build.Summary)
	}
}

// ps5Stopped reports whether the PS5 lane was stopped after job j was queued.
// Jobs queued after a stop are not affected by it.
func (a *App) ps5Stopped(j *BuildJob) bool {
	return atomic.LoadInt32(&a.ps5stop) != j.gen
}
