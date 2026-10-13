package main

import (
	"os"
	"path/filepath"
	"testing"
)

func cp(t *testing.T, src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dst), 0755)
	os.WriteFile(dst, b, 0644)
}

// mirrors the layout on Skelly's E: drive
func TestRealLayout(t *testing.T) {
	r := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	re := filepath.Join(r, "RESIDENT EVIL requiem [PPSA31246] v01.200.000", "PPSA31246-app")
	mkGame(t, re, "PPSA31246", "RESIDENT EVIL requiem", "01.200.000")
	os.MkdirAll(filepath.Join(re, "sce_module"), 0755)
	dl := filepath.Join(r, "duplex-resident evil requiem dlc ps5")
	os.MkdirAll(filepath.Join(dl, "PPSA31246-app0"), 0755)
	os.WriteFile(filepath.Join(dl, "PPSA31246-app0", "eboot.bin"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dl, "PPSA31246-app0", "re_dlc_000.pak"), []byte("x"), 0644)
	cp(t, "testdata/re_dlc.nfo", filepath.Join(dl, "_DUPLEX_", "duplex.nfo"))
	os.WriteFile(filepath.Join(dl, "_DUPLEX_", "RESIDENT.EVIL.requiem.Deluxe.Edition.DLC.Addon.PS5-DUPLEX.pkg"), []byte("\x7fFIH"+string(make([]byte, 300))), 0644)
	// PRAGMATA: real param.json, no eboot.bin
	pr := filepath.Join(r, "PRAGMATA [PPSA02530] v01.200.000", "PPSA02530-app")
	cp(t, "testdata/pragmata_param.json", filepath.Join(pr, "sce_sys", "param.json"))
	os.MkdirAll(filepath.Join(pr, "sce_module"), 0755)
	os.WriteFile(filepath.Join(pr, "re_chunk_000.pak"), []byte("x"), 0644)
	// Yotei with DLC already inside DLC\ (raw dump) + loose Yotei DLC
	y := filepath.Join(r, "Ghost of Yotei [PPSA26344] v01.008.000")
	mkGame(t, filepath.Join(y, "PPSA26344-app0"), "PPSA26344", "Ghost of Yōtei", "01.008.000")
	mkDLCDump(filepath.Join(y, "DLC", "PPSA26344-ac"), "UP9000-PPSA26344_00-YOTEISOUNDTRACK0", "Ghost of Yōtei Original Soundtrack")
	mkDLCDump(filepath.Join(r, "UP9000-PPSA26344_00-YOTEILEGENDS0000"), "UP9000-PPSA26344_00-YOTEILEGENDS0000", "Ghost of Yōtei Legends")
	// a game that includes its DLC must stay a game
	inc := filepath.Join(r, "duplex-ghost.of.yotei.incl.dlc.ps5")
	mkGame(t, filepath.Join(inc, "PPSA99001-app"), "PPSA99001", "Some Game", "01.000.000")

	g, _ := scan(r, nil, nil, defaultIgnored)
	o := NameOpts{Pattern: defaultPattern, DLCPattern: defaultDLCPattern, Safe: true, WrapLoose: true, MoveDLC: true}
	plan(g, o)
	for _, x := range g {
		kind := "game"
		if x.IsDLC {
			kind = "DLC "
			if x.InPlace {
				kind = "DLC*"
			}
		}
		t.Logf("%s %-46s id=%-9s -> %-62s | %s", kind, x.Current, x.TitleID, x.NewName, x.Status)
		if x.IsDLC && x.DLCInfo.Note != "" {
			t.Logf("       note: %s", x.DLCInfo.Note)
		}
	}
	ok, f, moves := renameAll(g, nil, o)
	t.Log("done", ok, "failed", f)
	listTree(t, r)
	g2, _ := scan(r, nil, nil, defaultIgnored)
	plan(g2, o)
	for _, x := range g2 {
		if x.IsDLC || len(x.DLC) > 0 {
			t.Logf("rescan: %-50s dlc=%d | %s", x.Current, len(x.DLC), x.Status)
		}
	}
	a, b := undo(moves)
	t.Log("undo", a, b)
	listTree(t, r)
}
