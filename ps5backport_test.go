package main

import (
	"os"
	"testing"
)

// LOADOUT_PS5_DIR=<game folder on the PS5> LOADOUT_PS5_IP=...: reads what its
// programs ask for, without changing anything.
func TestPS5BackportCheckLive(t *testing.T) {
	dir, ip := os.Getenv("LOADOUT_PS5_DIR"), os.Getenv("LOADOUT_PS5_IP")
	if dir == "" || ip == "" {
		t.Skip("set LOADOUT_PS5_DIR and LOADOUT_PS5_IP")
	}
	a := &App{}
	a.cfg.PS5IP, a.cfg.PS5Port, a.cfg.ConsoleFW = ip, 2121, "12.00"
	c, err := a.ps5BackportCheck(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s (fakelib files: %d)", c.Verdict, c.Explanation, c.Fakelib)
	for _, p := range c.Programs {
		t.Logf("  %-32s %-6s %s", p.Name, p.SDK, p.Kind)
	}
}
