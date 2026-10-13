package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkDLCDump(dir, contentID, name string) {
	os.MkdirAll(filepath.Join(dir, "sce_sys"), 0755)
	id := contentID[7:16]
	os.WriteFile(filepath.Join(dir, "sce_sys", "param.json"), []byte(`{"titleId":"`+id+`","contentId":"`+contentID+`","contentVersion":"01.000.000","localizedParameters":{"defaultLanguage":"en-US","en-US":{"titleName":"`+name+`"}}}`), 0644)
	os.WriteFile(filepath.Join(dir, "data.bin"), make([]byte, 3000), 0644)
}

func mkPkg(path, contentID string, ctype uint32) {
	h := make([]byte, 0x1000)
	binary.BigEndian.PutUint32(h, 0x7F434E54)
	copy(h[0x40:], contentID)
	binary.BigEndian.PutUint32(h[0x74:], ctype)
	os.WriteFile(path, h, 0644)
}

func listTree(t *testing.T, r string) {
	filepath.Walk(r, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(r, p)
		if rel != "." && strings.Count(rel, string(os.PathSeparator)) < 3 && !strings.Contains(rel, "sce_sys") {
			t.Log("    ", rel)
		}
		return nil
	})
}

func TestDLC(t *testing.T) {
	r := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// 1) loose game with DLC already inside a DLC folder
	y := filepath.Join(r, "Ghost of Yotei [PPSA26344] v01.008.000")
	mkGame(t, y, "PPSA26344", "Ghost of Yotei", "01.008.000")
	mkDLCDump(filepath.Join(y, "DLC", "PPSA26344-ac"), "UP9000-PPSA26344_00-YOTEISOUNDTRACK0", "Ghost of Yotei Soundtrack")
	// 2) loose DLC dump next to the games, for the same game
	mkDLCDump(filepath.Join(r, "UP9000-PPSA26344_00-YOTEILEGENDS0000"), "UP9000-PPSA26344_00-YOTEILEGENDS0000", "Ghost of Yotei Legends")
	// 3) PS4 game + loose DLC pkg for it + a PS4 game pkg that must stay a game file
	b := filepath.Join(r, "CUSA00419")
	os.MkdirAll(filepath.Join(b, "sce_sys"), 0755)
	os.WriteFile(filepath.Join(b, "eboot.bin"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(b, "sce_sys", "param.sfo"), sfoBytes("Bloodborne", "CUSA00419"), 0644)
	mkPkg(filepath.Join(r, "EP9000-CUSA00419_00-BBTOLDHUNTERS000-A0100-V0100.pkg"), "EP9000-CUSA00419_00-BBTOLDHUNTERS000", 0x1B)
	mkPkg(filepath.Join(r, "CUSA00419-patch.pkg"), "EP9000-CUSA00419_00-BLOODBORNE000000", 0x1A)
	// 4) scene release folder: game nested + DLC dump that sorts first
	d := filepath.Join(r, "duplex-ghost.of.yotei.incl.dlc.ps5")
	mkDLCDump(filepath.Join(d, "PPSA12345-ac"), "UP9000-PPSA12345_00-EXTRADLC00000000", "Extra DLC")
	mkGame(t, filepath.Join(d, "PPSA12345-app"), "PPSA12345", "Nested Game", "01.000.000")
	// 5) DLC we can't place
	os.MkdirAll(filepath.Join(r, "duplex-resident.evil.requiem.dlc.ps5"), 0755)
	os.WriteFile(filepath.Join(r, "duplex-resident.evil.requiem.dlc.ps5", "x.rar"), []byte("x"), 0644)
	mkDLCDump(filepath.Join(r, "UP0001-PPSA99999_00-ORPHANDLC0000000"), "UP0001-PPSA99999_00-ORPHANDLC0000000", "Orphan DLC")

	t.Log("BEFORE")
	listTree(t, r)
	g, _ := scan(r, nil, nil, defaultIgnored)
	o := NameOpts{Pattern: defaultPattern, Safe: true, WrapLoose: true, MoveDLC: true}
	plan(g, o)
	for _, x := range g {
		kind := "game"
		if x.IsDLC {
			kind = "DLC "
		}
		t.Logf("%s %-48s id=%-9s dlcInside=%d -> %-40s | %s", kind, x.Current, x.TitleID, len(x.DLC), filepath.Base(x.NewName), x.Status)
		for _, dd := range x.DLC {
			t.Logf("        inside: %s (%s, %s)", dd.Name, dd.Kind, dd.ContentID)
		}
	}
	ok, f, moves := renameAll(g, nil, o)
	t.Log("renamed", ok, "failed", f)
	t.Log("AFTER")
	listTree(t, r)
	a, bad := undo(moves)
	t.Log("undo", a, bad)
	t.Log("AFTER UNDO")
	listTree(t, r)
}
