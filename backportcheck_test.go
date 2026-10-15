package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSDKText(t *testing.T) {
	if s := sdkText(0x13000038); s != "13.00" {
		t.Fatal(s)
	}
	if v := sdkOfFirmware("12.00"); v != 0x1200FFFF {
		t.Fatalf("%x", v)
	}
	if v := sdkOfFirmware("9.60"); v != 0x0960FFFF {
		t.Fatalf("%x", v)
	}
	if !(0x12000038 <= sdkOfFirmware("12.00")) || 0x13000000 <= sdkOfFirmware("12.00") {
		t.Fatal("comparison")
	}
}

// LOADOUT_IMAGES=a.exfat;b.exfat (or folders) checks real games.
func TestBackportCheckLive(t *testing.T) {
	list := os.Getenv("LOADOUT_IMAGES")
	if list == "" {
		t.Skip("set LOADOUT_IMAGES")
	}
	for _, p := range filepath.SplitList(list) {
		var c *backportCheck
		var err error
		if st, e := os.Stat(p); e == nil && st.IsDir() {
			c, err = checkFolderBackport(p)
		} else {
			c, err = checkImageBackport(p)
		}
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		c.judge("12.00")
		t.Logf("%s\n  verdict %s: %s\n  fakelib files %d, unreadable %d", filepath.Base(p), c.Verdict, c.Explanation, c.Fakelib, c.Unreadable)
		for i, pr := range c.Programs {
			if i < 6 {
				t.Logf("    %-40s %-6s %s", pr.Name, pr.SDK, pr.Kind)
			}
		}
	}
}
