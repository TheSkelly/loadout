package main

import (
	"os"
	"testing"
)

func TestWikiAbout(t *testing.T) {
	if os.Getenv("TT_WIKIBASE") == "" {
		t.Skip()
	}
	for _, n := range []string{"FINAL FANTASY XVI", "SILENT HILL f", "God of War Ragnarök", "Totally Unknown Game 9"} {
		a, l, err := wikiAbout(n)
		t.Logf("%s -> %q %s %v", n, a, l, err)
	}
	if a, _, _ := wikiAbout("FINAL FANTASY XVI"); a == "" {
		t.Fatal("no match")
	}
	if a, _, _ := wikiAbout("Totally Unknown Game 9"); a != "" {
		t.Fatal("false match")
	}
	if wikiMatches("NHL 27", "NHL 26", "NHL 26 is an ice hockey video game") {
		t.Fatal("wrong year matched")
	}
	if !wikiMatches("The Last of Us™ Part I", "The Last of Us Part I", "The Last of Us Part I is a 2022 action-adventure game") {
		t.Fatal("should match")
	}
}
