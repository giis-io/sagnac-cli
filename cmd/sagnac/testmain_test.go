package main

import (
	"crypto/ed25519"
	"os"
	"testing"

	"github.com/giis-io/sagnac-cli/internal/kit"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

// testKitKey signs the kits served by the tests; TestMain pins its public key.
var testKitKey ed25519.PrivateKey

// The version check runs at every launch: tests never reach the real server.
// A closed local port refuses at once; a cache directory of their own keeps
// the user's cache out of it.
func TestMain(m *testing.M) {
	// The test binary is the relay of a check under Linux (oracle.RunRelay).
	if len(os.Args) > 1 && os.Args[1] == oracle.RelayCommand {
		os.Exit(oracle.RunRelay(os.Args[2:]))
	}
	dir, err := os.MkdirTemp("", "sagnac-test-cache-")
	if err != nil {
		panic(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	testKitKey = priv
	kit.SetTrustedKeys([]kit.PublicKey{{ID: kit.KeyID(pub), Key: pub}})
	// doctor never reads the machine that runs the tests.
	host = healthyHost()
	os.Setenv("SAGNAC_VERSION_URL", "http://127.0.0.1:1/cli/version.json")
	os.Setenv("XDG_CACHE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
