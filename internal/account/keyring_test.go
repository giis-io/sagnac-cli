package account

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeSecurity imitates /usr/bin/security for one generic password:
// "-i" reads commands on stdin, find prints the password or exits 44.
const fakeSecurity = `#!/bin/sh
d="$FAKE_DIR"
printf '%s\n' "$*" >> "$d/argv"
case "$1" in
-i)
  while IFS= read -r line; do
    printf '%s\n' "$line" >> "$d/stdin"
    set -- $line
    while [ $# -gt 0 ]; do
      if [ "$1" = "-w" ]; then printf '%s' "$2" > "$d/secret"; fi
      shift
    done
  done ;;
find-generic-password)
  [ -f "$d/secret" ] || { echo "security: The specified item could not be found in the keychain." >&2; exit 44; }
  cat "$d/secret"; echo ;;
delete-generic-password)
  [ -f "$d/secret" ] || exit 44
  rm "$d/secret" ;;
esac
`

// fakeSecretTool imitates secret-tool: store reads the secret on stdin,
// lookup prints it or exits 1 with nothing on stderr when there is none.
const fakeSecretTool = `#!/bin/sh
d="$FAKE_DIR"
printf '%s\n' "$*" >> "$d/argv"
case "$1" in
store) cat > "$d/secret" ;;
lookup) [ -f "$d/secret" ] || exit 1; cat "$d/secret" ;;
clear) rm -f "$d/secret" ;;
esac
`

func fakeTool(t *testing.T, script string) (tool, dir string) {
	t.Helper()
	dir = t.TempDir()
	tool = filepath.Join(dir, "tool")
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	return tool, dir
}

func exerciseStore(t *testing.T, store TokenStore, dir string) {
	t.Helper()
	if got, err := store.Load(); got != nil || err != nil {
		t.Fatalf("Load on empty keyring = %+v, %v", got, err)
	}
	tok := &Tokens{API: "http://127.0.0.1:8000", Access: "sgn_a_SECRETACCESS" + strings.Repeat("x", 300), Refresh: "sgn_r_SECRETREFRESH" + strings.Repeat("y", 300),
		ExpiresAt: time.Now().Add(time.Hour).Round(time.Second), Scope: "attempts:write"}
	if err := store.Save(tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil || got.Access != tok.Access || got.Refresh != tok.Refresh || !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	if strings.Contains(string(argv), "SECRET") {
		t.Errorf("a token appears on a command line (visible in ps):\n%s", argv)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := store.Load(); got != nil || err != nil {
		t.Errorf("Load after Delete = %+v, %v", got, err)
	}
	if err := store.Delete(); err != nil {
		t.Errorf("Delete twice: %v", err)
	}
}

func TestKeychainStoreSecretOnStdin(t *testing.T) {
	tool, dir := fakeTool(t, fakeSecurity)
	exerciseStore(t, &KeychainStore{Tool: tool, Account: "http://127.0.0.1:8000"}, dir)
}

func TestSecretServiceStoreSecretOnStdin(t *testing.T) {
	tool, dir := fakeTool(t, fakeSecretTool)
	exerciseStore(t, &SecretServiceStore{Tool: tool, Account: "http://127.0.0.1:8000"}, dir)
}

func TestKeyringFailureIsAnError(t *testing.T) {
	tool, _ := fakeTool(t, "#!/bin/sh\necho 'locked' >&2\nexit 51\n")
	for _, store := range []TokenStore{&KeychainStore{Tool: tool, Account: "a"}, &SecretServiceStore{Tool: tool, Account: "a"}} {
		if err := store.Save(expired("a")); err == nil {
			t.Errorf("%T: Save with a failing tool returned nil", store)
		}
		if _, err := store.Load(); err == nil {
			t.Errorf("%T: Load with a failing tool returned nil", store)
		}
	}
}

func TestOpenStoreFallsBackToAnnouncedFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("PATH", t.TempDir()) // no secret-tool
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	saved := securityTool
	securityTool = filepath.Join(t.TempDir(), "absent")
	defer func() { securityTool = saved }()

	store, err := OpenStore("https://sagnac.fr", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	fs, ok := store.(*FileStore)
	if !ok || fs.Path != filepath.Join(xdg, "sagnac", "tokens.json") {
		t.Fatalf("OpenStore without keyring = %#v", store)
	}
	c, err := NewClient("https://sagnac.fr", store)
	if err != nil || c.LockPath != filepath.Join(xdg, "sagnac", "tokens.lock") {
		t.Errorf("NewClient = %+v, %v", c, err)
	}
}

func TestOpenStorePrefersKeyring(t *testing.T) {
	tool, _ := fakeTool(t, fakeSecurity+"\n")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	switch runtime.GOOS {
	case "darwin":
		saved := securityTool
		securityTool = tool
		defer func() { securityTool = saved }()
		store, err := OpenStore("https://sagnac.fr", false, nil)
		if k, ok := store.(*KeychainStore); err != nil || !ok || k.Account != "https://sagnac.fr" {
			t.Errorf("OpenStore = %#v, %v; want the keychain", store, err)
		}
	case "linux":
		if err := os.Rename(tool, filepath.Join(filepath.Dir(tool), "secret-tool")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", filepath.Dir(tool))
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
		store, err := OpenStore("https://sagnac.fr", false, nil)
		if k, ok := store.(*SecretServiceStore); err != nil || !ok || k.Account != "https://sagnac.fr" {
			t.Errorf("OpenStore = %#v, %v; want the Secret Service", store, err)
		}
	}
}

// TestRealKeychain runs against the keychain of the session when asked:
// SAGNAC_TEST_KEYCHAIN=1 go test ./internal/account -run TestRealKeychain
func TestRealKeychain(t *testing.T) {
	if os.Getenv("SAGNAC_TEST_KEYCHAIN") != "1" {
		t.Skip("set SAGNAC_TEST_KEYCHAIN=1 to use the real keychain")
	}
	store, err := OpenStore("http://127.0.0.1:1", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*FileStore); ok {
		t.Fatal("no keychain found in this session")
	}
	exerciseStore(t, store, t.TempDir())
}

// fakeSecurityMax is fakeSecurity with the limit of "security -i": a line of
// more than 4000 bytes is not read to the end, and nothing says so.
const fakeSecurityMax = `#!/bin/sh
d="$FAKE_DIR"
printf '%s\n' "$*" >> "$d/argv"
case "$1" in
-i)
  while IFS= read -r line; do
    if [ "${#line}" -gt 4000 ]; then printf 'LONG\n' >> "$d/toolong"; exit 0; fi
    set -- $line
    while [ $# -gt 0 ]; do
      if [ "$1" = "-w" ]; then printf '%s' "$2" > "$d/secret"; fi
      shift
    done
  done ;;
find-generic-password)
  [ -f "$d/secret" ] || exit 44
  cat "$d/secret"; echo ;;
esac
`

func TestKeychainLineBoundedTo4000Bytes(t *testing.T) {
	tool, dir := fakeTool(t, fakeSecurityMax)
	store := &KeychainStore{Tool: tool, Account: "https://sagnac.fr"}
	// Largest tokens the client accepts: 512 + 512 characters and 256 of scope.
	big := &Tokens{API: "https://sagnac.fr", Access: strings.Repeat("a", 512), Refresh: strings.Repeat("r", 512),
		ExpiresAt: time.Now().Add(time.Hour).UTC(), Scope: strings.Repeat("s", 256)}
	if err := store.Save(big); err != nil {
		t.Fatalf("Save of the largest accepted tokens: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "toolong")); err == nil {
		t.Fatal("the largest accepted tokens exceed the 4000 bytes of security -i")
	}
	// Over the limit: refused before security runs.
	huge := *big
	huge.Scope = strings.Repeat("s", 3000)
	_ = os.Remove(filepath.Join(dir, "argv"))
	if err := store.Save(&huge); !errors.Is(err, ErrKeyringFailed) {
		t.Fatalf("Save over 4000 bytes = %v, want ErrKeyringFailed", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); err == nil {
		t.Error("security was run with a line over 4000 bytes")
	}
}
