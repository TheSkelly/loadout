package main

import (
	"errors"
	"sync/atomic"
)

// Controls for one job in the PS5 lane: stop it, pause it (and resume it
// later), or move it up or down the queue. Stop on the lane still stops
// everything; these touch only the job they're given. A paused send that
// went over FTP picks up where it was when resumed; one through PS5Upload
// starts its file again.

// jobByIDLocked finds a job by its id.
func (a *App) jobByIDLocked(id int) *BuildJob {
	for _, j := range a.build.Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// queuedAtLocked is where a job waits in the PS5 queue, or -1.
func (a *App) queuedAtLocked(j *BuildJob) int {
	for i, x := range a.ps5q {
		if x == j {
			return i
		}
	}
	return -1
}

// controllable: jobs of their own in the PS5 lane. A build's send rides on
// its build and is stopped with it.
func controllable(j *BuildJob) bool {
	return j != nil && j.Kind != "build" && j.Kind != "remove" && j.Kind != "unpack"
}

// jobAction applies stop, pause, resume, up or down to job id.
func (a *App) jobAction(id int, action string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	j := a.jobByIDLocked(id)
	if !controllable(j) {
		return errors.New("That job isn't in the PS5 queue.")
	}
	at := a.queuedAtLocked(j)
	switch action {
	case "stop", "pause":
		state := map[string]string{"stop": "cancelled", "pause": "paused"}[action]
		if at >= 0 { // still waiting: just take it out
			a.ps5q = append(a.ps5q[:at], a.ps5q[at+1:]...)
			j.State, j.Note = state, ""
			a.ps5cond.Broadcast()
			a.laneIdleLocked()
			return nil
		}
		if j.State == "paused" && action == "stop" {
			j.State = "cancelled" // a paused job that won't be resumed
			return nil
		}
		if j.State == "queued" || j.State == "done" || j.State == "failed" || j.State == "cancelled" || j.State == "paused" {
			return errors.New("That job isn't running.")
		}
		j.pause = action == "pause"
		atomic.StoreInt32(&j.stop, 1) // the running job sees this and winds down
		return nil
	case "resume":
		if j.State != "paused" && j.State != "cancelled" && j.State != "failed" {
			return errors.New("Only a paused, stopped or failed job can be started again.")
		}
		atomic.StoreInt32(&j.stop, 0)
		j.pause = false
		j.State, j.Note, j.Pct = "queued", "", 0
		a.enqueuePS5Locked(j)
		return nil
	case "up", "down":
		if at < 0 {
			return errors.New("Only a waiting job can be moved.")
		}
		to := at - 1
		if action == "down" {
			to = at + 1
		}
		if to < 0 || to >= len(a.ps5q) {
			return nil // already first or last
		}
		a.ps5q[at], a.ps5q[to] = a.ps5q[to], a.ps5q[at]
		return nil
	}
	return errors.New("unknown action")
}

// queueItem is one PS5 job as the phone page and the PS5 app show it.
type queueItem struct {
	ID    int    `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	State string `json:"state"`
	Pct   int    `json:"pct"`
	Note  string `json:"note"`
	Waits int    `json:"waits"` // place in the queue (1 is next), 0 when not waiting
}

// queueLocked is the PS5 lane: what's running, then what waits in order,
// then what's paused.
func (a *App) queueLocked() []queueItem {
	out := []queueItem{}
	item := func(j *BuildJob) queueItem {
		return queueItem{ID: j.ID, Key: j.Key, Name: j.Name, State: j.State, Pct: j.Pct, Note: j.Note}
	}
	waiting := map[*BuildJob]bool{}
	for _, j := range a.ps5q {
		waiting[j] = true
	}
	for _, j := range a.build.Jobs {
		if controllable(j) && !waiting[j] && j.State != "queued" && j.State != "paused" && j.State != "done" && j.State != "failed" &&
			j.State != "cancelled" && j.State != "skipped" {
			out = append(out, item(j))
		}
	}
	for i, j := range a.ps5q {
		if controllable(j) {
			it := item(j)
			it.Waits = i + 1
			out = append(out, it)
		}
	}
	for _, j := range a.build.Jobs {
		if controllable(j) && j.State == "paused" {
			out = append(out, item(j))
		}
	}
	return out
}

// laneIdleLocked marks the PS5 lane idle when taking a job out left nothing.
func (a *App) laneIdleLocked() {
	if len(a.ps5q) == 0 && a.ps5active == 0 && a.dropActive == 0 && a.build.PS5Busy {
		a.build.PS5Busy = false
		a.build.Sending, a.build.SendSpeed, a.build.SendETA, a.build.SendFile, a.build.SendSlow = "", 0, 0, "", ""
		a.finishLaneLocked("")
	}
}
