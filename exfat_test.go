package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExfatBuild(t *testing.T) {
	src := os.Getenv("EXSRC")
	dst := os.Getenv("EXDST")
	if src == "" {
		t.Skip("set EXSRC/EXDST")
	}
	p, err := PlanExfat(src, "PPSA02225")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("files=%d dirs=%d data=%d image=%d cluster=%d", p.Files, p.Dirs, p.DataBytes, p.TotalBytes, p.clusterSize)
	last := int64(0)
	if err := p.Write(dst, func(d, tot int64, cur string) {
		if d-last > 20<<20 || cur == "" {
			last = d
			t.Logf("  %3d%% %s", d*100/tot, filepath.Base(cur))
		}
	}, nil); err != nil {
		t.Fatal(err)
	}
}
