package main

import (
	"sync"
	"testing"
)

func TestJobControls(t *testing.T) {
	a := &App{}
	a.ps5cond = sync.NewCond(&a.mu)
	jobs := []*BuildJob{{Kind: "send", Name: "A", State: "queued"}, {Kind: "send", Name: "B", State: "queued"}, {Kind: "send", Name: "C", State: "queued"}}
	a.mu.Lock()
	a.addJobsLocked(jobs)
	for _, j := range jobs {
		a.enqueuePS5Locked(j)
	}
	a.mu.Unlock()
	order := func() string {
		s := ""
		for _, j := range a.ps5q {
			s += j.Name
		}
		return s
	}
	if err := a.jobAction(jobs[2].ID, "up"); err != nil || order() != "ACB" {
		t.Fatalf("C up: %v, queue %s", err, order())
	}
	if err := a.jobAction(jobs[0].ID, "down"); err != nil || order() != "CAB" {
		t.Fatalf("A down: %v, queue %s", err, order())
	}
	// stopping one waiting job takes only that one out
	if err := a.jobAction(jobs[1].ID, "stop"); err != nil || order() != "CA" || jobs[1].State != "cancelled" {
		t.Fatalf("B stop: %v, queue %s, B %s", err, order(), jobs[1].State)
	}
	if err := a.jobAction(jobs[0].ID, "pause"); err != nil || order() != "C" || jobs[0].State != "paused" {
		t.Fatalf("A pause: %v, queue %s, A %s", err, order(), jobs[0].State)
	}
	if err := a.jobAction(jobs[0].ID, "resume"); err != nil || order() != "CA" || jobs[0].State != "queued" {
		t.Fatalf("A resume: %v, queue %s, A %s", err, order(), jobs[0].State)
	}
	// a running job is asked to wind down, and only it
	a.mu.Lock()
	a.ps5q = a.ps5q[1:] // C starts running
	jobs[2].State = "sending"
	a.mu.Unlock()
	if err := a.jobAction(jobs[2].ID, "pause"); err != nil || !a.ps5Stopped(jobs[2]) || a.ps5Stopped(jobs[0]) {
		t.Fatalf("pausing the running job: %v, C stopped %v, A stopped %v", err, a.ps5Stopped(jobs[2]), a.ps5Stopped(jobs[0]))
	}
	if a.jobAction(9999, "stop") == nil {
		t.Error("an unknown job was accepted")
	}
}
