package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFTP(t *testing.T) {
	if os.Getenv("FTPTEST") == "" {
		t.Skip()
	}
	root := "/tmp/claude-0/ps5root"
	local := filepath.Join(t.TempDir(), "Ghost of Yotei [PPSA26344] v01.008.000.exfat")
	data := make([]byte, 23<<20+12345)
	for i := range data {
		data[i] = byte(i * 7)
	}
	os.WriteFile(local, data, 0644)
	f, err := ftpDial("127.0.0.1", 2121)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dest := "/data/etaHEN/games"
	// 1) fresh upload (folder doesn't exist yet)
	_, err = f.upload(local, dest, func(s, tot int64) {}, nil)
	got, _ := os.ReadFile(filepath.Join(root, dest, filepath.Base(local)))
	t.Log("fresh upload:", err, "match:", bytes.Equal(got, data))
	// 2) partial copy on the PS5 -> resume
	remote := filepath.Join(root, dest, filepath.Base(local))
	os.Truncate(remote, 9<<20)
	var first int64 = -1
	resumed, err := f.upload(local, dest, func(s, tot int64) {
		if first < 0 {
			first = s
		}
	}, nil)
	got, _ = os.ReadFile(remote)
	t.Logf("resume: resumed=%v startedAt=%d err=%v match=%v", resumed, first, err, bytes.Equal(got, data))
	// 3) already there -> nothing sent
	n := 0
	_, err = f.upload(local, dest, func(s, tot int64) { n++ }, nil)
	t.Log("repeat send: err", err, "progress calls", n)
	// 4) cancel half way, then list
	calls := 0
	_, err = f.upload(local+"", "/data/etaHEN/other", func(s, tot int64) { calls++ }, func() bool { return calls > 3 })
	t.Log("cancel:", err)
	ents, err := f.list("/data/etaHEN")
	t.Log("list:", ents, err)
	ents, err = f.list("/data/etaHEN/games")
	t.Log("list games:", ents, err)
	_, err = f.list("/nope")
	t.Log("list missing:", err)
	_, err = ftpDial("127.0.0.1", 2199)
	t.Log("bad port:", err)
}
