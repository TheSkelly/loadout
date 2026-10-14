package main

import (
	"os"
	"testing"
)

func TestNewerContent(t *testing.T) {
	cases := []struct {
		cat, inst string
		want      bool
	}{
		{"01.000.070", "01.000.060", true},
		{"01.000.070", "01.000.070", false},
		{"01.000.070", "01.000.080", false},
		{"", "01.000.010", false},
		{"01.000.070", "1.0", false},
	}
	for _, c := range cases {
		if got := newerContent(c.cat, c.inst); got != c.want {
			t.Errorf("newerContent(%q, %q) = %v", c.cat, c.inst, got)
		}
	}
}

// LOADOUT_LIVE=1 reads the real catalog and checks its signature.
func TestHomebrewCatalogLive(t *testing.T) {
	if os.Getenv("LOADOUT_LIVE") == "" {
		t.Skip("set LOADOUT_LIVE=1 to read homebrew.page")
	}
	base, m, apps, err := hbLoad(true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s sequence %d, %d apps", base, m.Sequence, len(apps))
	for _, x := range apps {
		t.Logf("%s %-28s %-10s %s %s", x.TitleID, x.Name, x.Status, strOr(x.Format), strOr(x.ContentVersion))
	}
	if _, err := hbAppFile(apps[0].TitleID); err != nil && apps[0].Status == "available" {
		t.Fatal(err)
	}
}
