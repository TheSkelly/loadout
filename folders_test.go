package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGameFolders(t *testing.T) {
	main, more := t.TempDir(), t.TempDir()
	a := &App{cfg: Settings{Folder: main, MoreFolders: []string{more, strings.ToUpper(more), filepath.Join(more, "unplugged"), " "}}}
	got := a.gameFoldersLocked()
	if len(got) != 2 || got[0] != main || got[1] != more {
		t.Fatalf("gameFoldersLocked = %q, want main then the one other folder that exists", got)
	}
}

func TestImageDirFor(t *testing.T) {
	main, more := t.TempDir(), t.TempDir()
	a := &App{cfg: Settings{Folder: main, MoreFolders: []string{more}}}
	if got := a.imageDirForLocked(&Game{Path: filepath.Join(more, "Some Game [PPSA01234]")}); got != filepath.Join(more, "exFAT Images") {
		t.Errorf("a game in the second folder builds into %s, want that folder's own exFAT Images", got)
	}
	if got := a.imageDirForLocked(&Game{Path: filepath.Join(main, "Other Game [PPSA05678]")}); got != filepath.Join(main, "exFAT Images") {
		t.Errorf("a game in the main folder builds into %s, want the main exFAT Images", got)
	}
}

// A DLC name Loadout made must read back the same, or every scan renames it
// again (it used to gain a " -" each time).
func TestDLCNameSettles(t *testing.T) {
	o := NameOpts{DLCPattern: defaultDLCPattern, Safe: true}
	for _, start := range []string{"Resident Evil Requiem Deluxe Edition DLC Addon", "RESIDENT EVIL requiem [PPSA31246] DLC - - - - - v01", "Bonus Pack v2"} {
		name := buildDLCName(o, "RESIDENT EVIL requiem", "PPSA31246", DLC{Name: start})
		again := buildDLCName(o, "RESIDENT EVIL requiem", "PPSA31246", DLC{Name: name})
		if again != name {
			t.Errorf("%q names itself %q, then %q", start, name, again)
		}
	}
	if got := buildDLCName(o, "RESIDENT EVIL requiem", "PPSA31246", DLC{Name: "RESIDENT EVIL requiem [PPSA31246] DLC - Deluxe Edition"}); got != "RESIDENT EVIL requiem [PPSA31246] DLC - Deluxe Edition" {
		t.Errorf("a name Loadout made changed to %q", got)
	}
}

func TestWrappedDLCFolderName(t *testing.T) {
	dir := t.TempDir()
	wrap := filepath.Join(dir, "RESIDENT EVIL requiem [PPSA31246] DLC - - v01")
	inner := filepath.Join(wrap, "DLC", "RESIDENT EVIL requiem [PPSA31246] DLC - Deluxe Edition")
	os.MkdirAll(filepath.Join(inner, "sce_sys"), 0755)
	os.WriteFile(filepath.Join(inner, "data.bin"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(inner, "sce_sys", "param.json"), []byte(`{"titleId":"PPSA31246"}`), 0644)
	d, _ := identifyDLCFolder(wrap, filepath.Base(wrap))
	o := NameOpts{DLCPattern: defaultDLCPattern, Safe: true}
	if got := buildDLCName(o, "RESIDENT EVIL requiem", "PPSA31246", d); got != "RESIDENT EVIL requiem [PPSA31246] DLC - Deluxe Edition" {
		t.Errorf("the wrapper folder is named %q (from %q)", got, d.Name)
	}
}

func TestEmptyDLCFolderIgnored(t *testing.T) {
	wrap := filepath.Join(t.TempDir(), "RESIDENT EVIL requiem [PPSA31246] DLC - - v01")
	os.MkdirAll(filepath.Join(wrap, "DLC", "RESIDENT EVIL requiem [PPSA31246] DLC - Deluxe Edition"), 0755)
	if _, ok := identifyDLCFolder(wrap, filepath.Base(wrap)); ok {
		t.Error("an empty folder was taken for DLC")
	}
}
