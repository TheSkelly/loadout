package main

import (
	"os"
	osexec "os/exec"
	"sync/atomic"
	"testing"
)

// Runs only against the local fake PS5 (TT_LIVE=host:ftpport).
func TestFastLive(t *testing.T) {
	if os.Getenv("TT_LIVE") == "" {
		t.Skip()
	}
	img := os.Getenv("TT_IMG")
	up := func(streams int, stopAt int64) (bool, int, error) {
		f, err := ftpDial("127.0.0.1", 2121)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var hit int32
		return fastUpload(f, "127.0.0.1", img, os.Getenv("TT_DIR"), streams, func(s, tot int64) {
			if stopAt > 0 && s >= stopAt && atomic.CompareAndSwapInt32(&hit, 0, 1) {
				runCmd("sh", "-c", "kill $(cat /tmp/claude-0/recv.pid)")
			}
		}, nil)
	}
	if !recvPing("127.0.0.1") {
		t.Fatal("no receiver")
	}
	if os.Getenv("TT_BREAK") != "" {
		_, _, err := up(4, 300<<20)
		t.Log("broken run:", err)
		if err == nil {
			t.Fatal("expected a failure")
		}
		runCmd("sh", "-c", "cd /tmp/claude-0 && TT_ROOT=/tmp/claude-0/ps5root nohup ./ttrecv-linux >/dev/null 2>&1 & echo $! > /tmp/claude-0/recv.pid; sleep 1")
	}
	res, used, err := up(4, 0)
	t.Log("resumed", res, "streams", used, "err", err)
	if err != nil {
		t.Fatal(err)
	}
}

func runCmd(name string, args ...string) {
	c := osexec.Command(name, args...)
	c.Run()
}
