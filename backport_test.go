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

func TestBackportState(t *testing.T) {
	for _, c := range []struct {
		name  string
		look  gameLook
		known bool
		sdk   string
		want  string
	}{
		{"built for newer firmware, not backported", gameLook{SDK: 0x13000038}, true, "13.00", "needs"},
		{"NHL 27: fakelib, but its programs still ask for 13.00", gameLook{SDK: 0x13000038, Fakelib: true}, true, "13.00", "incomplete"},
		{"Wolverine: programs lowered, fakelib", gameLook{SDK: 0x04000038, Fakelib: true}, true, "13.00", "ok"},
		{"built for this firmware", gameLook{SDK: 0x11000040}, true, "11.00", ""},
		{"not read yet, param.json says newer", gameLook{}, false, "13.00", ""},
		{"not read yet, param.json says older", gameLook{}, false, "11.00", ""},
	} {
		if got, _ := backportState(c.look, c.known, c.sdk, "12.00"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got, _ := backportState(gameLook{SDK: 0x13000038}, true, "13.00", ""); got != "" {
		t.Errorf("no firmware set: %q", got)
	}
}
