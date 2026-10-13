package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStoreIcons(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/US/en/") && strings.HasSuffix(r.URL.Path, "CUSA03979_00"):
			fmt.Fprintf(w, `{"name":"BioShock: The Collection","images":[{"type":12,"url":"%s/b.jpg"},{"type":10,"url":"%s/icon.png"}]}`, srv.URL, srv.URL)
		case r.URL.Path == "/icon.png":
			w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	storeBase = srv.URL
	if !fetchStoreIconsWith(srv.Client(), []string{"CUSA03979", "PPSA99999"}) {
		t.Fatal("no icon saved")
	}
	if !fileExists(ps5IconPath("CUSA03979")) || fileExists(ps5IconPath("PPSA99999")) {
		t.Fatal("wrong icons", ps5IconPath("CUSA03979"))
	}
}
