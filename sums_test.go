package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSumsVerify(t *testing.T) {
	src := "/tmp/claude-0/lib2/God of War Ragnarok [PPSA08330] v05.005.000"
	if !dirExists(src) {
		t.Skip()
	}
	img := filepath.Join(t.TempDir(), "x.exfat")
	p, err := PlanExfat(src, "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Write(img, nil, nil); err != nil {
		t.Fatal(err)
	}
	// the take-turns mode must produce the very same image
	img2 := filepath.Join(t.TempDir(), "y.exfat")
	p2, _ := PlanExfat(src, "TEST")
	p2.SameDisk, p2.BatchBytes = true, 64<<20
	if err := p2.Write(img2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExfatSums(img2, src, p2.Sums, nil, nil); err != nil {
		t.Fatal("batch image:", err)
	}
	if err := VerifyExfat(img2, src, nil, nil); err != nil {
		t.Fatal("batch image full compare:", err)
	}
	if len(p.Sums) == 0 {
		t.Fatal("no sums")
	}
	if err := VerifyExfat(img, src, nil, nil); err != nil {
		t.Fatal("full compare:", err)
	}
	if err := VerifyExfatSums(img, src, p.Sums, nil, nil); err != nil {
		t.Fatal("sum compare:", err)
	}
	// damage the image in the middle of the data
	f, _ := os.OpenFile(img, os.O_RDWR, 0)
	fi, _ := f.Stat()
	for off := fi.Size() / 2; off < fi.Size()/2+(8<<20); off += 4096 {
		f.WriteAt([]byte("XXXXXXXXXXXXXXXX"), off)
	}
	f.Close()
	if err := VerifyExfatSums(img, src, p.Sums, nil, nil); err == nil {
		t.Fatal("damage not caught")
	} else {
		t.Log("caught:", err)
	}
}
