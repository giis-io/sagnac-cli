package account

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestHTTPRejectedOutsideLoopback(t *testing.T) {
	accepted := map[string]string{
		"":                             "https://sagnac.fr",
		"https://sagnac.fr":            "https://sagnac.fr",
		"https://sagnac.fr/":           "https://sagnac.fr",
		"HTTPS://api.example.org:8443": "https://api.example.org:8443",
		"http://127.0.0.1:8000":        "http://127.0.0.1:8000",
		"http://localhost:8000/":       "http://localhost:8000",
		"http://LOCALHOST":             "http://LOCALHOST",
		"  http://127.0.0.1:9  ":       "http://127.0.0.1:9",
	}
	for raw, want := range accepted {
		t.Setenv(envAPI, raw)
		got, err := URLAPI()
		if err != nil || got != want {
			t.Errorf("URLAPI(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	refused := []string{
		"http://sagnac.fr",
		"http://127.0.0.2:8000",
		"http://localhost.evil.example",
		"http://[::1]:8000",
		"http://127.0.0.1.nip.io",
		"ftp://sagnac.fr",
		"sagnac.fr",
		"//sagnac.fr",
		"https://user:pass@sagnac.fr",
		"http://127.0.0.1@evil",
		"http://127.0.0.1@evil.example:8000",
		"https://sagnac.fr/#frag",
		"javascript:alert(1)",
		"https://",
	}
	for _, raw := range refused {
		t.Setenv(envAPI, raw)
		if got, err := URLAPI(); !errors.Is(err, ErrInsecureURL) {
			t.Errorf("URLAPI(%q) = %q, %v; want ErrInsecureURL", raw, got, err)
		}
		t.Setenv(envVersionURL, raw)
		if got, err := URLVersion(); !errors.Is(err, ErrInsecureURL) {
			t.Errorf("URLVersion(%q) = %q, %v; want ErrInsecureURL", raw, got, err)
		}
	}
	// The API is a base address: a path or a query would be silently dropped.
	for _, raw := range []string{"https://sagnac.fr/api", "https://sagnac.fr/?x=1"} {
		t.Setenv(envAPI, raw)
		if got, err := URLAPI(); !errors.Is(err, ErrInsecureURL) {
			t.Errorf("URLAPI(%q) = %q, %v; want ErrInsecureURL", raw, got, err)
		}
	}

	t.Setenv(envVersionURL, "")
	if got, err := URLVersion(); err != nil || got != "https://sagnac.fr/cli/version.json" {
		t.Errorf("URLVersion default = %q, %v", got, err)
	}
	t.Setenv(envVersionURL, "http://127.0.0.1:8000/cli/version.json")
	if got, err := URLVersion(); err != nil || got != "http://127.0.0.1:8000/cli/version.json" {
		t.Errorf("URLVersion loopback = %q, %v", got, err)
	}
	t.Setenv(envVersionURL, "http://cdn.example.org/cli/version.json")
	if _, err := URLVersion(); !errors.Is(err, ErrInsecureURL) {
		t.Errorf("URLVersion over http outside loopback accepted: %v", err)
	}
}

func TestConfigDirIsUnderTheConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, err := ConfigDir(); err != nil || got != filepath.Join(xdg, "sagnac") {
		t.Errorf("ConfigDir = %q, %v", got, err)
	}
}

var reUUID4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestStableDeviceID(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	// Several commands started together get the same identifier.
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := DeviceID()
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("DeviceID differs between calls: %v", ids)
		}
	}
	if !reUUID4.MatchString(ids[0]) {
		t.Fatalf("DeviceID = %q, want a UUID v4", ids[0])
	}

	path := filepath.Join(xdg, "sagnac", "device_id")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != ids[0] {
		t.Errorf("device_id holds %q, want %q", raw, ids[0])
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)

	again, err := DeviceID()
	if err != nil || again != ids[0] {
		t.Errorf("second DeviceID = %q, %v; want %q", again, err, ids[0])
	}

	// A damaged file is replaced by a new identifier, never sent as is.
	if err := os.WriteFile(path, []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := DeviceID()
	if err != nil || !reUUID4.MatchString(fresh) {
		t.Fatalf("DeviceID after damage = %q, %v", fresh, err)
	}
	if kept, _ := DeviceID(); kept != fresh {
		t.Errorf("replacement identifier not kept: %q then %q", fresh, kept)
	}
}

func TestDeviceNameWithoutHostname(t *testing.T) {
	name := DeviceName()
	if name != runtime.GOOS+"-"+runtime.GOARCH {
		t.Errorf("DeviceName = %q", name)
	}
	// Same pattern as the server (appareils.jetons.NOM_APPAREIL, fullmatch).
	if !regexp.MustCompile(`^(darwin|linux)-(amd64|arm64)$`).MatchString(name) {
		t.Errorf("DeviceName %q refused by the server pattern", name)
	}
	if host, err := os.Hostname(); err == nil && host != "" && strings.Contains(strings.ToLower(name), strings.ToLower(host)) {
		t.Errorf("DeviceName %q contains the host name %q", name, host)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode %o, want %o", path, got, want)
	}
}
