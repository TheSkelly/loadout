package main

import (
	"os"
	"testing"
)

func TestTagContent(t *testing.T) {
	for tag, want := range map[string]string{"v1.005.000": "01.005.000", "1.2.3": "01.002.003", "v12.010.200-alpha": "12.010.200", "nightly": ""} {
		if got := tagContent(tag); got != want {
			t.Errorf("%s: %q, want %q", tag, got, want)
		}
	}
	if !newerContent(tagContent("v1.005.000"), "01.002.000") {
		t.Error("1.005 should be newer than 01.002.000")
	}
}

// LOADOUT_LIVE=1: reads ProsperoEden's newest release from GitHub.
func TestGitHubLatestLive(t *testing.T) {
	if os.Getenv("LOADOUT_LIVE") == "" {
		t.Skip("set LOADOUT_LIVE=1")
	}
	r, err := ghLatest("Swordpdf/ProsperoEden", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s (%s) %d bytes, sha %s, pre %v\n%s", r.Tag, r.Content, r.Size, r.SHA, r.Pre, r.URL)
	if r, ok := ghNewer("PPSA99008", "01.002.000", false); !ok || r.Content != tagContent(r.Tag) {
		t.Fatal("expected a newer release than 01.002.000")
	}
}
