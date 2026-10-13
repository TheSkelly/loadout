package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestForwarderBuild(t *testing.T) {
	zp := os.Getenv("TT_FWDZIP")
	if zp == "" {
		t.Skip("no sample forwarder")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := fwdSetTemplate(zp); err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("TT_FWDOUT")
	root, err := buildForwarder(out, "PPSA91234", fwdSpec{Target: "PPSA99008", Rom: "bbb-h-aabpa.xci", Title: "Mario Kart 8 Deluxe", Icon: os.Getenv("TT_FWDICON"), Exit: true})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(os.Getenv("TT_FWDSAMPLE"))
	got, _ := os.ReadFile(filepath.Join(root, "sce_sys", "pic0.dds"))
	if !bytes.Equal(want[:148], got[:148]) || len(want) != len(got) {
		t.Fatalf("dds header or size differs: %d vs %d", len(want), len(got))
	}
	// keep the soft backdrop as a PNG so the encoded file can be compared with it
	ic, _ := loadPicture(filepath.Join(root, "sce_sys", "icon0.png"))
	f, _ := os.Create(filepath.Join(out, "backdrop.png"))
	png.Encode(f, softBackdrop(ic, 3840, 2160))
	f.Close()
	if os.Getenv("TT_FWDICON") != "" {
		src, _ := loadPicture(os.Getenv("TT_FWDICON"))
		photo := cropFit(src, 3840, 2160)
		os.WriteFile(filepath.Join(out, "photo.dds"), ddsBC7(photo), 0644)
		f, _ := os.Create(filepath.Join(out, "photo.png"))
		png.Encode(f, photo)
		f.Close()
	}
}
