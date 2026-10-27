package main

import "testing"

const sampleVersionXML = `<?xml version="1.0" encoding="UTF-8"?><title_patch nptitleid="PPSA02530_00">
<app_tag content_id="UP0102-PPSA02530_00-X" revision="18"><package content_ver="01.300.000" mandatory="false"/></app_tag>
<ac_tag content_id="UP0102-PPSA02530_00-DLC1" revision="7"><package content_ver="01.000.000"/></ac_tag></title_patch>`

func TestParseLatestAppVersion(t *testing.T) {
	got, err := parseLatestAppVersion([]byte(sampleVersionXML))
	if err != nil || got != "01.300.000" {
		t.Fatalf("got %q, %v", got, err)
	}
	two := `<title_patch><app_tag><package content_ver="01.100.000"/><package content_ver="01.200.000"/></app_tag></title_patch>`
	if got, _ := parseLatestAppVersion([]byte(two)); got != "01.200.000" {
		t.Errorf("newest of several = %q", got)
	}
	if _, err := parseLatestAppVersion([]byte(`<title_patch><ac_tag><package content_ver="01.000.000"/></ac_tag></title_patch>`)); err == nil {
		t.Error("DLC-only file should report no app version")
	}
	if _, err := parseLatestAppVersion([]byte("not xml")); err == nil {
		t.Error("garbage should fail")
	}
}

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		latest, have string
		want         bool
	}{
		{"01.300.000", "01.200.000", true},
		{"01.200.000", "01.200.000", false},
		{"01.100.000", "01.200.000", false},
		{"01.010.000", "01.009.000", true},
		{"02.000.000", "01.999.999", true},
		{"01.30", "01.300.000", false},
		{"", "01.000.000", false},
		{"01.100.000", "", false},
		{"x.y", "01.000.000", false},
	}
	for _, c := range cases {
		if got := versionNewer(c.latest, c.have); got != c.want {
			t.Errorf("versionNewer(%q,%q)=%v want %v", c.latest, c.have, got, c.want)
		}
	}
}

func TestVersionURIAllowed(t *testing.T) {
	ok := "https://sgst.prod.dl.playstation.net/sgst/prod/00/np/PPSA02530_00/a-version.xml"
	if !versionURIAllowed(ok) {
		t.Error("Sony's address should be allowed")
	}
	for _, bad := range []string{"", "http://sgst.prod.dl.playstation.net/x.xml", "https://evil.example.com/x.xml",
		"https://playstation.net.evil.com/x.xml", "https://127.0.0.1/x.xml", "file:///c:/x.xml"} {
		if versionURIAllowed(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestParamVersionURI(t *testing.T) {
	m, err := readParamJSON("testdata/pragmata_param.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.VersionURI != "https://sgst.prod.dl.playstation.net/sgst/prod/00/np/PPSA02530_00/61a74a99-40ae-4f1f-906a-4dac6b046451-version.xml" {
		t.Errorf("VersionURI = %q (should be trimmed)", m.VersionURI)
	}
}
