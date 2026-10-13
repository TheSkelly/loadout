package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImageHasFakelib(t *testing.T) {
	for _, withFakelib := range []bool{true, false} {
		src := t.TempDir()
		os.MkdirAll(filepath.Join(src, "sce_sys"), 0755)
		os.WriteFile(filepath.Join(src, "eboot.bin"), []byte("elf"), 0644)
		os.WriteFile(filepath.Join(src, "sce_sys", "param.json"), []byte("{}"), 0644)
		if withFakelib {
			os.MkdirAll(filepath.Join(src, "fakelib"), 0755)
			os.WriteFile(filepath.Join(src, "fakelib", "libSceAgc.sprx"), []byte("prx"), 0644)
		}
		plan, err := PlanExfat(src, "PPSA01234")
		if err != nil {
			t.Fatal(err)
		}
		img := filepath.Join(t.TempDir(), "game.exfat")
		if err := plan.Write(img, nil, nil); err != nil {
			t.Fatal(err)
		}
		got, err := imageHasFakelib(img)
		if err != nil || got != withFakelib {
			t.Errorf("image with fakelib=%v: found %v (err %v)", withFakelib, got, err)
		}
	}
}

func TestNeedsBackport(t *testing.T) {
	for _, c := range []struct {
		sdk, sysver string
		backported  bool
		want        bool
	}{
		{"13.00", "13.60", false, true},  // built for newer firmware, nothing to make up for it
		{"13.00", "13.60", true, false},  // has its fakelibs
		{"12.00", "13.60", false, false}, // asks for 13.60, but built for this firmware: it runs
		{"11.00", "12.70", false, false},
	} {
		e := &LibEntry{Platform: "PS5", SDK: c.sdk, SysVer: c.sysver}
		if got := needsBackport(e, "12.00", c.backported); got != c.want {
			t.Errorf("SDK %s, asks %s, backported %v: needs backport = %v, want %v", c.sdk, c.sysver, c.backported, got, c.want)
		}
	}
}
