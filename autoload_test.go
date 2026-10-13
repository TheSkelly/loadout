package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPayloadVersion(t *testing.T) {
	fs, _ := filepath.Glob("/tmp/claude-0/al/*.elf")
	if len(fs) == 0 {
		t.Skip()
	}
	for _, f := range fs {
		b, _ := os.ReadFile(f)
		t.Logf("%s -> %q", filepath.Base(f), payloadVersion(b))
	}
}
