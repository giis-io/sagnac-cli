package main

import (
	"os"
	"testing"
)

// The version check runs at every launch: tests never reach the real server.
// A closed local port refuses at once; a cache directory of their own keeps
// the user's cache out of it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sagnac-test-cache-")
	if err != nil {
		panic(err)
	}
	os.Setenv("SAGNAC_VERSION_URL", "http://127.0.0.1:1/cli/version.json")
	os.Setenv("XDG_CACHE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
