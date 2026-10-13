package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerify(t *testing.T) {
	src := os.Getenv("EXSRC")
	if src == "" {
		t.Skip()
	}
	img := filepath.Join(t.TempDir(), "x.exfat")
	p, err := PlanExfat(src, "PPSA00001")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Write(img, nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Log("clean image:", VerifyExfat(img, src, nil, nil))

	// flip one byte deep inside a file's data
	f, _ := os.OpenFile(img, os.O_RDWR, 0)
	fi, _ := f.Stat()
	off := fi.Size() - 5000
	b := make([]byte, 1)
	f.ReadAt(b, off)
	b[0] ^= 0xFF
	f.WriteAt(b, off)
	f.Close()
	t.Log("corrupted image:", VerifyExfat(img, src, nil, nil))
	p.Write(img, nil, nil)

	extra := filepath.Join(src, "ui", "added-later.txt")
	os.WriteFile(extra, []byte("new"), 0644)
	t.Log("file added to folder:", VerifyExfat(img, src, nil, nil))
	os.Remove(extra)
	t.Log("removed again:", VerifyExfat(img, src, nil, nil))

	// a truncated image (e.g. copy interrupted)
	os.Truncate(img, fi.Size()-1<<20)
	t.Log("truncated image:", VerifyExfat(img, src, nil, nil))

	// removable root checks
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "Game [PPSA1]", "PPSA00001-app", "sce_sys"), 0755)
	t.Log("wrapped only:", removableRoot(filepath.Join(r, "Game [PPSA1]"), filepath.Join(r, "Game [PPSA1]", "PPSA00001-app")))
	os.MkdirAll(filepath.Join(r, "Game [PPSA1]", "DLC stuff"), 0755)
	t.Log("with extra DLC folder:", removableRoot(filepath.Join(r, "Game [PPSA1]"), filepath.Join(r, "Game [PPSA1]", "PPSA00001-app")))
	t.Log("root is folder:", removableRoot(filepath.Join(r, "Game [PPSA1]"), filepath.Join(r, "Game [PPSA1]")))
}
