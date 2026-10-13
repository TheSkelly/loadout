package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkGame(t *testing.T, dir, id, name, ver string) {
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "eboot.bin"), []byte("x"), 0644)
	p := filepath.Join(dir, "sce_sys", "param.json")
	os.MkdirAll(filepath.Dir(p), 0755)
	os.WriteFile(p, []byte(`{"titleId":"`+id+`","contentVersion":"`+ver+`","localizedParameters":{"defaultLanguage":"en-US","en-US":{"titleName":"`+name+`"}}}`), 0644)
}

func TestScreenshotFolder(t *testing.T) {
	r := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, d := range []string{"Compressed", "Compressed Temp"} {
		os.MkdirAll(filepath.Join(r, d), 0755)
	}
	mkGame(t, filepath.Join(r, "Downloading", "PPSA99999-app"), "PPSA99999", "Half Downloaded Game", "01.000.000")
	mkGame(t, filepath.Join(r, "DRAGON QUEST I & II HD-2D Remake 01.000 PPSA22322"), "PPSA22322", "DRAGON QUEST I & II HD-2D Remake", "01.000.000")
	mkGame(t, filepath.Join(r, "duplex-ghost.of.yotei.incl.dlc.ps5", "PPSA12345-app"), "PPSA12345", "Ghost of Yōtei", "01.002.000")
	os.MkdirAll(filepath.Join(r, "duplex-resident.evil.requiem.ps5"), 0755) // just rar files inside, no metadata
	mkGame(t, filepath.Join(r, "FINAL FANTASY XVI 01.003.004 PPSA10664"), "PPSA10664", "FINAL FANTASY XVI", "01.003.004")
	mkGame(t, filepath.Join(r, "Ghost of Tsushima"), "PPSA01782", "Ghost of Tsushima DIRECTOR'S CUT", "01.000.000")
	mkGame(t, filepath.Join(r, "LoU 1 - PPSA03396-app0"), "PPSA03396", "The Last of Us™ Part I", "01.000.000")
	os.MkdirAll(filepath.Join(r, "PPSA01442 - Devil May Cry 5 SE"), 0755)
	mkGame(t, filepath.Join(r, "PPSA03016"), "PPSA03016", "Clair Obscur: Expedition 33", "01.000.000")
	os.MkdirAll(filepath.Join(r, "Ragnarok - PPSA08330-app0"), 0755)
	// eboot-only dump with nptitle.dat + pfs-version.dat
	os.MkdirAll(filepath.Join(r, "PPSA02572", "sce_sys"), 0755)
	os.WriteFile(filepath.Join(r, "PPSA02572", "eboot.bin"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(r, "PPSA02572", "sce_sys", "nptitle.dat"), []byte("Stellar Blade\r\n"), 0644)
	os.WriteFile(filepath.Join(r, "PPSA02572", "pfs-version.dat"), []byte("01.006.000\n"), 0644)
	// demo with PPLH id and only eboot.bin, no id-less false positives
	os.MkdirAll(filepath.Join(r, "PPLH01234-demo"), 0755)
	os.WriteFile(filepath.Join(r, "PPLH01234-demo", "eboot.bin"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(r, "PRAGMATA 01.200 PPSA02530"), 0755)
	os.WriteFile(filepath.Join(r, "Gears of War Reloaded-PPSA17525.rar"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(r, "PPSA03016.zip"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(r, "notes.txt"), []byte("x"), 0644)

	g, err := scan(r, nil, nil, defaultIgnored)
	if err != nil {
		t.Fatal(err)
	}
	plan(g, NameOpts{Pattern: defaultPattern, Safe: true})
	for _, x := range g {
		t.Logf("%-42s -> %-48s %s", x.Current, x.NewName, x.Status)
	}
	ok, f, _ := renameAll(g, nil, NameOpts{Pattern: defaultPattern, Safe: true})
	t.Log("renamed", ok, "failed", f)
	es, _ := os.ReadDir(r)
	for _, e := range es {
		t.Log("  ", e.Name())
	}
}

func TestPatterns(t *testing.T) {
	cases := []struct {
		o                       NameOpts
		name, id, ver, platform string
	}{
		{NameOpts{Pattern: "{TITLE} [{ID}] v{VERSION}", Safe: false}, "DRAGON QUEST I & II HD-2D Remake", "PPSA22322", "01.000.000", "PS5"},
		{NameOpts{Pattern: "{TITLE} [{ID}] v{VERSION}", Safe: true}, "DRAGON QUEST I & II HD-2D Remake", "PPSA22322", "01.000.000", "PS5"},
		{NameOpts{Pattern: "{TITLE} [{ID}] v{VERSION}", Safe: true}, "Marvel's Spider-Man 2™", "PPSA01284", "", "PS5"},
		{NameOpts{Pattern: "{TITLE} ({ID}) ({VERSION})", Safe: true}, "Ghost of Yōtei", "PPSA12345", "", "PS5"},
		{NameOpts{Pattern: "{ID} - {TITLE} v{VERSION}", Safe: true}, "Clair Obscur: Expedition 33", "PPSA03016", "01.004.000", "PS5"},
		{NameOpts{Pattern: "{TITLE} [{ID}] [{PLATFORM}] v{VERSION}", Safe: true}, "Bloodborne™", "CUSA00419", "01.09", "PS4"},
		{NameOpts{Pattern: "{TITLE}", Safe: true}, "No ID Pattern", "PPSA00001", "1.0", "PS5"},
		{NameOpts{Pattern: "{TITLE} [{ID}]", Safe: true}, "ドラゴンクエスト Remake!", "PPSA00002", "", "PS5"},
	}
	for _, c := range cases {
		t.Logf("%-42q safe=%-5v -> %q", c.o.Pattern, c.o.Safe, buildName(c.o, c.name, c.id, c.ver, c.platform))
	}
	for _, fw := range [][2]string{{"9.60", "10.00"}, {"10.01", "9.60"}, {"7.61", "7.61"}, {"9.6", "9.60"}} {
		e := &LibEntry{Platform: "PS5", SysVer: fw[1]}
		t.Logf("console %s game %s needs newer: %v", fw[0], fw[1], needsNewerFW(e, fw[0]))
	}
}

func tree(t *testing.T, r string) {
	filepath.Walk(r, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(r, p)
		if rel != "." && strings.Count(rel, string(os.PathSeparator)) < 2 {
			t.Log("   ", rel)
		}
		return nil
	})
}

func TestWrap(t *testing.T) {
	r := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Screenshot 1: already wrapped -> leave alone
	mkGame(t, filepath.Join(r, "Ghost of Tsushima [PPSA02225] v02.024.000", "PPSA02225-app"), "PPSA02225", "Ghost of Tsushima", "02.024.000")
	// Screenshot 2 style, already named: just wrap
	d := filepath.Join(r, "Alan Wake 2 [PPSA02572] v01.200.007")
	mkGame(t, d, "PPSA02572", "Alan Wake 2", "01.200.007")
	os.WriteFile(filepath.Join(d, "eboot.bin"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(d, "movies"), 0755)
	os.WriteFile(filepath.Join(d, "++ build-r-retail 0711"), nil, 0644)
	// loose AND badly named: wrap + rename
	d2 := filepath.Join(r, "PPSA13428")
	mkGame(t, d2, "PPSA13428", "Horizon Zero Dawn Remastered", "01.008.001")
	os.WriteFile(filepath.Join(d2, "eboot.bin"), []byte("x"), 0644)

	g, _ := scan(r, nil, nil, defaultIgnored)
	o := NameOpts{Pattern: defaultPattern, Safe: true, WrapLoose: true}
	plan(g, o)
	for _, x := range g {
		t.Logf("%-44s wrap=%-5v -> %-44s %s", x.Current, x.Wrap, x.NewName, x.Status)
	}
	ok, f, moves := renameAll(g, nil, o)
	t.Log("ok", ok, "failed", f, "moves", len(moves))
	for _, x := range g {
		t.Logf("  status: %s", x.Status)
	}
	tree(t, r)
	// rescan: nothing should be left to do
	g2, _ := scan(r, nil, nil, defaultIgnored)
	plan(g2, o)
	for _, x := range g2 {
		t.Logf("rescan %-44s %s", x.Current, x.Status)
	}
	a, b := undo(moves)
	t.Log("undo", a, b)
	tree(t, r)
}

func sfoBytes(title, id string) []byte {
	keys := []string{"TITLE", "TITLE_ID"}
	vals := []string{title, id}
	var kt, dt []byte
	var ents []byte
	le := binary.LittleEndian
	for i, k := range keys {
		e := make([]byte, 16)
		le.PutUint16(e[0:], uint16(len(kt)))
		le.PutUint16(e[2:], 0x0204)
		v := append([]byte(vals[i]), 0)
		le.PutUint32(e[4:], uint32(len(v)))
		le.PutUint32(e[8:], 128)
		le.PutUint32(e[12:], uint32(len(dt)))
		kt = append(append(kt, k...), 0)
		dt = append(dt, v...)
		dt = append(dt, make([]byte, 128-len(v))...)
		ents = append(ents, e...)
	}
	h := make([]byte, 20)
	copy(h, "\x00PSF")
	le.PutUint32(h[4:], 0x101)
	le.PutUint32(h[8:], uint32(20+len(ents)))
	le.PutUint32(h[12:], uint32(20+len(ents)+len(kt)))
	le.PutUint32(h[16:], 2)
	return append(append(append(h, ents...), kt...), dt...)
}
