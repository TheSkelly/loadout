package main

import (
	"os"
	"testing"
)

func TestRootRel(t *testing.T) {
	for _, c := range [][3]string{
		{"/data/etaHEN/games", "/data", "etaHEN/games"},
		{"/user/data/games", "/data", "games"},
		{"/mnt/usb0", "/mnt/usb0", ""},
		{"/mnt/usb1/homebrew/games/", "/mnt/usb1", "homebrew/games"},
		{"/mnt/ext1/etaHEN/games", "/mnt/ext1", "etaHEN/games"},
	} {
		if r, l := rootOf(c[0]), relOf(c[0]); r != c[1] || l != c[2] {
			t.Errorf("%s: got %q %q", c[0], r, l)
		}
	}
}

func TestParseDF(t *testing.T) {
	out := "Filesystem   Size  Used  Avail  Capacity  Mounted on\r\n/dev/ssd0.user   667.2G   412.3G   254.9G     61%  /user\r\n/dev/da0s1  931.5G  100.0G  831.5G  10%  /mnt/usb0\r\n/dev/x 6.0G 5.1G 0.9G 85% /\r\n/$ "
	e := parseDF(out)
	if len(e) != 3 {
		t.Fatal(e)
	}
	d, ok := dfFor(e, "/data/etaHEN/games")
	if !ok || d.Mount != "/user" || d.Avail/(1<<30) != 254 {
		t.Fatal(d, ok)
	}
	if _, ok := dfFor(e, "/mnt/usb1/x"); ok {
		t.Fatal("usb1 should not match /")
	}
}

func TestProbeLive(t *testing.T) {
	if os.Getenv("FTPTEST") == "" {
		t.Skip()
	}
	info := probePS5("127.0.0.1", 2121, func(r string) string { return r + "/etaHEN/games" })
	t.Logf("online=%v shell=%v err=%q", info.Online, info.Shell, info.Err)
	for _, d := range info.Drives {
		t.Logf("%-28s %-10s free=%s total=%s empty=%v dir=%s", d.Label, d.Root, humanSize(d.Free), humanSize(d.Total), d.Empty, d.Dir)
	}
}

func TestMakeFakeSFO(t *testing.T) {
	if p := os.Getenv("MKSFO"); p != "" {
		os.WriteFile(p, sfoBytes("Some PS4 Game", "CUSA03979"), 0644)
	}
}

func TestInstalledScan(t *testing.T) {
	if os.Getenv("FTPTEST") == "" {
		t.Skip()
	}
	f, err := ftpDial("127.0.0.1", 2121)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	names := map[string]string{}
	g, _ := scanPS5Games(f, []PS5Drive{{Root: "/data", Kind: "internal", Short: "Internal"}}, names, nil)
	for id, cs := range g {
		for _, c := range cs {
			t.Logf("%s %s %s name=%q", id, c.Kind, c.Path, c.Name)
		}
	}
}
