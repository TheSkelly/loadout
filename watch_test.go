package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestFindArchiveSets(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-time.Hour)
	write := func(rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte("x"), 0644)
		os.Chtimes(p, old, old)
	}
	// an old-style set in its release folder, with its .sfv
	write("Diablo.PS5-DUPLEX/duplex-diablo.ps5.rar")
	write("Diablo.PS5-DUPLEX/duplex-diablo.ps5.r00")
	write("Diablo.PS5-DUPLEX/duplex-diablo.ps5.s00")
	write("Diablo.PS5-DUPLEX/duplex-diablo.ps5.nfo")
	os.WriteFile(filepath.Join(root, "Diablo.PS5-DUPLEX", "duplex-diablo.ps5.sfv"), []byte("; made by\nduplex-diablo.ps5.rar 0a0b0c0d\nduplex-diablo.ps5.r00 0a0b0c0d\nduplex-diablo.ps5.s00 0a0b0c0d\n"), 0644)
	os.Chtimes(filepath.Join(root, "Diablo.PS5-DUPLEX", "duplex-diablo.ps5.sfv"), old, old)
	// a .partNN set
	write("Zelda.NSW/bbb.part01.rar")
	write("Zelda.NSW/bbb.part02.rar")
	// a split ZIP lying loose, and a movie
	write("Game.PS5.zip.001")
	write("Game.PS5.zip.002")
	write("Movie.2026.mkv")
	// a set whose .sfv names a part that hasn't arrived
	write("Half.PS5-GRP/half.rar")
	os.WriteFile(filepath.Join(root, "Half.PS5-GRP", "half.sfv"), []byte("half.rar 01020304\nhalf.r00 01020304\n"), 0644)

	sets := map[string]archiveSet{}
	for _, s := range findArchiveSets(root) {
		sets[filepath.Base(s.first)] = s
	}
	var firsts []string
	for k := range sets {
		firsts = append(firsts, k)
	}
	sort.Strings(firsts)
	want := []string{"Game.PS5.zip.001", "bbb.part01.rar", "duplex-diablo.ps5.rar", "half.rar"}
	if len(firsts) != len(want) {
		t.Fatalf("first parts = %v, want %v", firsts, want)
	}
	for i := range want {
		if firsts[i] != want[i] {
			t.Fatalf("first parts = %v, want %v", firsts, want)
		}
	}
	if s := sets["duplex-diablo.ps5.rar"]; s.name != "Diablo.PS5-DUPLEX" || len(s.parts) < 3 {
		t.Errorf("diablo set = %+v", s)
	}
	if s := sets["Game.PS5.zip.001"]; s.name != "Game.PS5" || len(s.parts) != 2 {
		t.Errorf("loose zip set = %+v", s)
	}
	if ok, _ := downloadDone(sets["duplex-diablo.ps5.rar"]); !ok {
		t.Error("a complete set isn't seen as downloaded")
	}
	if ok, why := downloadDone(sets["half.rar"]); ok {
		t.Error("a set missing a part is seen as downloaded")
	} else if why != "waiting for half.r00" {
		t.Errorf("why = %q", why)
	}
	write("Zelda.NSW/bbb.part03.rar.part")
	if ok, _ := downloadDone(sets["bbb.part01.rar"]); ok {
		t.Error("a set with a part still downloading is seen as downloaded")
	}
}

func TestCheckDownloadsExtracts(t *testing.T) {
	if sevenZip() == "" {
		t.Skip("7-Zip isn't installed")
	}
	dl, games := t.TempDir(), t.TempDir()
	old := time.Now().Add(-time.Hour)
	mk := func(name string, files map[string]string) {
		dir := filepath.Join(dl, name)
		os.MkdirAll(dir, 0755)
		f, _ := os.Create(filepath.Join(dir, "release.zip"))
		w := zip.NewWriter(f)
		for n, body := range files {
			x, _ := w.Create(n)
			x.Write([]byte(body))
		}
		w.Close()
		f.Close()
		os.Chtimes(filepath.Join(dir, "release.zip"), old, old)
	}
	mk("Some.Game.PS5-GRP", map[string]string{"PPSA01234-app0/sce_sys/param.json": `{"titleId":"PPSA01234"}`, "PPSA01234-app0/eboot.bin": "elf"})
	mk("Some.Movie.2026", map[string]string{"movie.mkv": "video"})
	a := &App{cfg: Settings{WatchFolder: dl, Folder: games}, lib: &Library{Games: map[string]*LibEntry{}}, scanning: true}
	a.checkDownloads()
	if !fileExists(filepath.Join(games, "Some.Game.PS5-GRP", "PPSA01234-app0", "eboot.bin")) {
		t.Fatalf("the game wasn't extracted into the games folder: %+v", a.watch)
	}
	if _, err := os.Stat(filepath.Join(games, "Some.Movie.2026")); err == nil {
		t.Error("a movie was extracted")
	}
	recs := loadWatchRecords()
	if len(recs) != 2 {
		t.Errorf("records = %+v", recs)
	}
	// a second check leaves it alone
	os.RemoveAll(filepath.Join(games, "Some.Game.PS5-GRP"))
	a.checkDownloads()
	if _, err := os.Stat(filepath.Join(games, "Some.Game.PS5-GRP")); err == nil {
		t.Error("an extracted download was extracted again")
	}
}
