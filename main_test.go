package main

import (
	"os"
	"testing"
)

// TestMain keeps every test away from the real settings folder. Tests set
// XDG_CONFIG_HOME, which only redirects os.UserConfigDir on Linux; on Windows
// it reads APPDATA, so point that at a throwaway folder too.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "loadout-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("APPDATA", tmp)
	os.Setenv("XDG_CONFIG_HOME", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
