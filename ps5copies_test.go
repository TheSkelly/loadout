package main

import "testing"

func TestDeletableCopiesSkipInstalledGames(t *testing.T) {
	got := deletableCopies([]PS5Copy{
		{Drive: "Internal", Path: "/data/etaHEN/games/A.exfat", Kind: "image", Size: 5},
		{Drive: "Ext 2", Path: "/mnt/usb1/etaHEN/games/A", Kind: "folder", Size: 9},
		{Drive: "Internal", Path: "/user/app/PPSA1", Kind: "installed"},
	})
	if len(got) != 2 || got[0].Path != "/data/etaHEN/games/A.exfat" || got[1].Kind != "folder" {
		t.Fatalf("got %+v, want the image and the folder only", got)
	}
	if deletableCopies(nil) == nil {
		t.Fatal("want an empty list, not nil, so the app gets [] in JSON")
	}
}
