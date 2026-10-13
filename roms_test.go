package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRomEmu(t *testing.T) {
	for system, want := range map[string]string{"Switch": edenID, "nsw": edenID, "ProsperoEden": edenID, "SNES": retroArchID, "ppsa99169": retroArchID, "PPSA12345": "PPSA12345"} {
		if id, _ := romEmu(system); id != want {
			t.Errorf("romEmu(%q) = %s, want %s", system, id, want)
		}
	}
}

func TestScanRoms(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, make([]byte, n), 0644)
	}
	write("Switch/Zelda_Tears [0100F2C0115B6000][v0] (USA).nsp", 10)
	write("Switch/readme.txt", 1)
	write("PS1/Final Fantasy VII (Disc 1)/ff7.cue", 2)
	write("PS1/Final Fantasy VII (Disc 1)/ff7.bin", 3)
	write("Empty/.hidden", 1)
	systems := scanRoms(root)
	if len(systems) != 2 || systems[0].Name != "PS1" || systems[1].Name != "Switch" {
		t.Fatalf("systems = %+v", systems)
	}
	ff := systems[0].Games[0]
	if !ff.IsDir || ff.Name != "Final Fantasy VII" || ff.Size != 5 || ff.Emu != "RetroArch" || ff.Key != "PS1/Final Fantasy VII (Disc 1)" {
		t.Errorf("folder game = %+v", ff)
	}
	sw := systems[1].Games
	if len(sw) != 1 || sw[0].Name != "Zelda Tears" || sw[0].Emu != "ProsperoEden" {
		t.Errorf("switch games = %+v", sw)
	}
}
