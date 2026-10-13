package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackExfat(t *testing.T) {
	src := t.TempDir()
	big := make([]byte, 300000) // spans several clusters
	rand.Read(big)
	files := map[string][]byte{
		"eboot.bin":                 big,
		"sce_sys/param.json":        []byte(`{"titleId":"PPSA01234"}`),
		"data/levels/one/map.bin":   []byte("map"),
		"data/empty.txt":            {},
		"Name With Spaces (v2).pak": []byte("pak"),
	}
	for n, b := range files {
		p := filepath.Join(src, filepath.FromSlash(n))
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, b, 0644)
	}
	plan, err := PlanExfat(src, "PPSA01234")
	if err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "game.exfat")
	if err := plan.Write(img, nil, nil); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "unpacked")
	var last, all int64
	if err := unpackExfat(img, out, func(d, tot int64) { last, all = d, tot }, nil); err != nil {
		t.Fatal(err)
	}
	if last != all || all != int64(len(big))+int64(len(files["sce_sys/param.json"]))+3+3 {
		t.Errorf("progress ended at %d of %d", last, all)
	}
	for n, want := range files {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(n)))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s came out different (err %v)", n, err)
		}
	}
	// and what came out is what went in: it verifies against the image
	if err := VerifyExfat(img, out, nil, nil); err != nil {
		t.Errorf("the folder doesn't match its image: %v", err)
	}
}
