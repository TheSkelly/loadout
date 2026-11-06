package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestImageIconReadsIcon0FromImage(t *testing.T) {
	icon := []byte("\x89PNG\r\n\x1a\nfake-icon-bytes")
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sce_sys"), 0755)
	os.WriteFile(filepath.Join(src, "eboot.bin"), []byte("elf"), 0644)
	os.WriteFile(filepath.Join(src, "sce_sys", "icon0.png"), icon, 0644)
	plan, err := PlanExfat(src, "PPSA01234")
	if err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "Game [PPSA01234] v01.000.000.exfat")
	if err := plan.Write(img, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := imageIcon(img)
	if err != nil || !bytes.Equal(got, icon) {
		t.Fatalf("icon = %q, err %v; want the file's bytes", got, err)
	}
}

func TestImageIconMissingIsAnError(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "eboot.bin"), []byte("elf"), 0644)
	plan, _ := PlanExfat(src, "PPSA01234")
	img := filepath.Join(t.TempDir(), "g.exfat")
	if err := plan.Write(img, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := imageIcon(img); err == nil {
		t.Fatal("expected an error for an image with no icon0.png")
	}
}

func TestTitleIDInImageName(t *testing.T) {
	for in, want := range map[string]string{
		"NHL 27 [PPSA34063] v01.003.001.exfat": "PPSA34063",
		"no id here.exfat":                     "",
	} {
		if got := titleIDInName(in); got != want {
			t.Errorf("titleIDInName(%q) = %q, want %q", in, got, want)
		}
	}
}
