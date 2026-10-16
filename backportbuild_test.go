package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackportTargets(t *testing.T) {
	cases := map[string][2]uint32{"12.00": {0x12000040, 0x13090001}, "10.01": {0x10010040, 0x12090001}, "4.03": {0x04030040, 0x09040001}}
	for fw, want := range cases {
		a, b, err := backportTargets(fw)
		if err != nil || a != want[0] || b != want[1] {
			t.Errorf("%s: %x %x %v, want %x %x", fw, a, b, err, want[0], want[1])
		}
	}
}

// readFromImage pulls one file out of an exFAT image.
func readFromImage(t *testing.T, image, path string) []byte {
	im, root, err := openExImage(image)
	if err != nil {
		t.Fatal(err)
	}
	defer im.f.Close()
	ents := root
	parts := strings.Split(path, "/")
	for i, part := range parts {
		var hit *exEntry
		for k := range ents {
			if strings.EqualFold(ents[k].name, part) {
				hit = &ents[k]
			}
		}
		if hit == nil {
			t.Fatalf("%s not in %s", path, image)
		}
		if i < len(parts)-1 {
			ents, _ = im.listDir(hit.first, hit.length, hit.noChain)
			continue
		}
		runs, _ := im.runs(hit.first, hit.length, hit.noChain)
		r := &exFileReader{im: im, runs: runs, size: int64(hit.length)}
		b, err := io.ReadAll(io.NewSectionReader(r, 0, int64(hit.length)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return nil
}

// LOADOUT_SELF_IMAGE=<image> LOADOUT_SELF_FILE=eboot.bin: the program comes
// back out as the ELF it was made from (make_fself gives the same bytes back).
func TestFselfRoundTrip(t *testing.T) {
	image, file := os.Getenv("LOADOUT_SELF_IMAGE"), os.Getenv("LOADOUT_SELF_FILE")
	if image == "" {
		t.Skip("set LOADOUT_SELF_IMAGE")
	}
	self := readFromImage(t, image, file)
	elf, err := fselfToELF(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "x.elf"), filepath.Join(dir, "x.self")
	os.WriteFile(in, elf, 0644)
	cmd := exec.Command("python", "-I", "tools/make_fself.py", in, out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make_fself: %v\n%s", err, b)
	}
	again, _ := os.ReadFile(out)
	t.Logf("SELF %d bytes, ELF %d bytes, re-signed %d bytes", len(self), len(elf), len(again))
	if bytes.Equal(again, self) {
		t.Log("byte-identical to the original")
		return
	}
	n := 0
	for n < len(self) && n < len(again) && self[n] == again[n] {
		n++
	}
	// the one thing allowed to differ is the 32-byte digest of the ELF in the
	// signing info: a SELF doesn't carry the padding between segments, so the
	// rebuilt ELF hashes differently while every byte the console loads matches
	diff, last := 0, 0
	for i := range self {
		if i < len(again) && self[i] != again[i] {
			diff, last = diff+1, i
		}
	}
	if len(self) == len(again) && last-n < 32 {
		t.Logf("matches the original except the ELF digest (%d bytes at 0x%x)", diff, n)
		return
	}
	t.Errorf("differs from the original at byte 0x%x (lengths %d vs %d)", n, len(self), len(again))
}

// LOADOUT_SELF_IMAGE=<image of a game built for a newer firmware>: its
// eboot.bin and libc.prx are backported to 12.00 in a scratch folder.
func TestRunBackport(t *testing.T) {
	image := os.Getenv("LOADOUT_SELF_IMAGE")
	if image == "" {
		t.Skip("set LOADOUT_SELF_IMAGE")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sce_module"), 0755)
	for _, f := range []string{"eboot.bin", "sce_module/libc.prx"} {
		os.WriteFile(filepath.Join(root, f), readFromImage(t, image, f), 0644)
	}
	a := &App{}
	a.cfg.ConsoleFW = "12.00"
	j := &BuildJob{Name: "test"}
	note, err := a.runBackport(j, root, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Log(note)
	c, _ := checkFolderBackport(root)
	c.judge("12.00")
	for _, p := range c.Programs {
		t.Logf("  %-22s now asks for %s (%s)", p.Name, p.SDK, p.Kind)
	}
	if c.highestValue > sdkOfFirmware("12.00") {
		t.Fatalf("still asks for %s", c.Highest)
	}
	// the patched program still turns back into an ELF the same size as before
	before, _ := fselfToELF(readFromImage(t, image, "eboot.bin"))
	after, err := fselfToELF(mustRead(t, filepath.Join(root, "eboot.bin")))
	if err != nil || len(before) != len(after) {
		t.Fatalf("eboot.bin: %v, %d vs %d bytes", err, len(before), len(after))
	}
	diff := 0
	for i := range before {
		if before[i] != after[i] {
			diff++
		}
	}
	t.Logf("eboot.bin as an ELF: %d bytes changed (the SDK fields)", diff)
	if diff == 0 || diff > 8 {
		t.Fatalf("expected only the SDK fields to change, %d bytes did", diff)
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLowerParamFirmware(t *testing.T) {
	in := []byte("{\n  \"titleId\": \"PPSA03912\",\n  \"requiredSystemSoftwareVersion\": \"0x1360000000000000\",\n  \"sdkVersion\": \"0x0300000000000000\"\n}")
	if fw := paramFirmware(in); fw != "13.60" {
		t.Fatalf("read %q", fw)
	}
	out, old, changed := lowerParamFirmware(in, "12.00")
	if !changed || old != "13.60" || paramFirmware(out) != "12.00" {
		t.Fatalf("%v %q %s", changed, old, out)
	}
	if string(out) != strings.Replace(string(in), "0x1360000000000000", "0x1200000000000000", 1) {
		t.Fatalf("changed more than the field:\n%s", out)
	}
	if _, _, changed := lowerParamFirmware(out, "12.00"); changed {
		t.Fatal("lowered twice")
	}
	if paramFirmware([]byte(`{"requiredSystemSoftwareVersion": "0x0300000000000000"}`)) != "3.00" {
		t.Fatal("3.00")
	}
}
