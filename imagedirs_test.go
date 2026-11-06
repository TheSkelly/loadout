package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A folder added to Loadout may be full of .exfat images itself (not hold a
// subfolder named "exFAT Images"), and they must show up.
func TestImagesDirectlyInAddedFolderAreIndexed(t *testing.T) {
	games, imgs := t.TempDir(), t.TempDir()
	name := "Diablo II - Resurrected [PPSA03912] v01.039.000.exfat"
	os.WriteFile(filepath.Join(imgs, name), []byte("x"), 0644)
	os.WriteFile(filepath.Join(imgs, "notes.txt"), []byte("x"), 0644)

	a := &App{cfg: Settings{Folder: games, MoreFolders: []string{imgs}}}
	got := a.imageIndex()["id:PPSA03912"]
	if got == "" || !strings.EqualFold(filepath.Base(got), name) {
		t.Fatalf("image in an added folder not found, index gave %q", got)
	}
}

func TestImagesInGamesFolderAreIndexed(t *testing.T) {
	games := t.TempDir()
	os.WriteFile(filepath.Join(games, "Game [PPSA00001] v01.000.000.exfat"), []byte("x"), 0644)
	a := &App{cfg: Settings{Folder: games}}
	if a.imageIndex()["id:PPSA00001"] == "" {
		t.Fatal("image sitting in the main games folder not found")
	}
}
