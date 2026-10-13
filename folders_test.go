package main

import (
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
