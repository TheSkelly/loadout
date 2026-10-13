package main

import (
	"os"
	"testing"
)

func TestSQLiteReader(t *testing.T) {
	p := os.Getenv("TT_SQLITETEST")
	if p == "" {
		t.Skip("no sample database")
	}
	main, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	wal, _ := os.ReadFile(p + "-wal")
	db, err := openSQLite(main, wal)
	if err != nil {
		t.Fatal(err)
	}
	tabs, err := db.tables()
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tabs {
		rows, err := db.rows(tb, 100000)
		if err != nil {
			t.Fatal(tb.Name, err)
		}
		t.Logf("%s cols=%v rows=%d", tb.Name, tb.Cols, len(rows))
		if len(rows) > 0 {
			t.Logf("  first=%v", rows[0])
			t.Logf("  last=%v", rows[len(rows)-1])
		}
	}
}

func TestFlipFlag(t *testing.T) {
	p := os.Getenv("TT_FLIPTEST")
	if p == "" {
		t.Skip("no sample database")
	}
	main, _ := os.ReadFile(p)
	out, ch, err := flipFlag(main, "tbl_iconinfo_0300567296", "titleId", "NPXS40047", "visible", false)
	if err != nil || !ch {
		t.Fatal(err, ch)
	}
	out2, ch, err := flipFlag(out, "tbl_concepticoninfo_0300567296", "localConceptId", "cid:local:NPXS40047", "validFlag", false)
	if err != nil || !ch {
		t.Fatal(err, ch)
	}
	diff := 0
	for i := range main {
		if main[i] != out2[i] {
			diff++
		}
	}
	t.Log("bytes changed:", diff)
	os.WriteFile(p+".patched", out2, 0644)
	back, _, _ := flipFlag(out2, "tbl_iconinfo_0300567296", "titleId", "NPXS40047", "visible", true)
	back, _, _ = flipFlag(back, "tbl_concepticoninfo_0300567296", "localConceptId", "cid:local:NPXS40047", "validFlag", true)
	if string(back) != string(main) {
		t.Fatal("undo didn't give the original back")
	}
}
