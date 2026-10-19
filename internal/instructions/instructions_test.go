package instructions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSafePageURL(t *testing.T) {
	const api = "https://sagnac.fr"
	good := []string{
		"https://sagnac.fr/labs/hello/enonce",
		"https://sagnac.fr/labs/hello/enonce?version=3",
		"HTTPS://SAGNAC.FR/labs/hello/enonce",
	}
	for _, u := range good {
		if got, err := SafePageURL(api, "hello", u); err != nil || got != u {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	bad := []string{
		"", "javascript:alert(1)", "file:///etc/passwd", "-a", "http://sagnac.fr/labs/hello/enonce",
		"https://evil.example/labs/hello/enonce", "https://sagnac.fr.evil.example/labs/hello/enonce",
		"https://user@sagnac.fr/labs/hello/enonce", "https://sagnac.fr:444/labs/hello/enonce",
		"https://sagnac.fr/labs/autre/enonce", "https://sagnac.fr/labs/hello/enonce/../x",
		"https://sagnac.fr/labs/hello/enonce#frag", "https://sagnac.fr/labs/hello/enonce?next=https://evil.example",
		"https://sagnac.fr/labs/hello/enonce?version=3&token=x", "https://sagnac.fr/labs/hello/enonce?",
		"https://sagnac.fr/labs/hello/%65nonce", "https://sagnac.fr/labs/hello/enonce\n", "https://sagnac.fr/labs/hello/enonce\x1b[31m",
		"//sagnac.fr/labs/hello/enonce", "/labs/hello/enonce", "https://sagnac.fr/appareil/",
	}
	for _, u := range bad {
		if _, err := SafePageURL(api, "hello", u); !errors.Is(err, ErrForeignPage) {
			t.Errorf("%q accepted", u)
		}
	}
	// In development, http on the loopback is the origin.
	if _, err := SafePageURL("http://127.0.0.1:8000", "hello", "http://127.0.0.1:8000/labs/hello/enonce?version=2"); err != nil {
		t.Errorf("dev origin: %v", err)
	}
	if _, err := SafePageURL("http://127.0.0.1:8000", "hello", "http://127.0.0.1:8001/labs/hello/enonce"); err == nil {
		t.Error("other port accepted")
	}
}

func TestInstalledVersion(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("work/hello/meta.yaml", "id: hello\ntitre: Bonjour\nversion: 5\nnested:\n  version: 9\n")
	write("work/hello/checks/verify.sh", "#!/bin/sh\n")
	write("work/autre/meta.yaml", "id: ailleurs\nversion: 2\n")
	write("work/vide/meta.yaml", "id: vide\n")
	write("work/neg/meta.yaml", "id: neg\nversion: -1\n")
	cases := []struct {
		start, lab string
		want       int
		ok         bool
	}{
		{"work", "hello", 5, true},               // ./<lab>/meta.yaml
		{"work/hello", "hello", 5, true},         // inside the lab
		{"work/hello/checks", "hello", 5, true},  // above the current directory
		{"work/autre", "autre", 0, false},        // meta of another lab
		{"work", "vide", 0, false},               // no version
		{"work", "neg", 0, false},                // invalid version
		{"work", "absent", 0, false},             // not installed
		{"work/hello/checks", "autre", 0, false}, // id differs: ignored
	}
	for _, c := range cases {
		got, ok := InstalledVersion(filepath.Join(root, filepath.FromSlash(c.start)), c.lab)
		if got != c.want || ok != c.ok {
			t.Errorf("%s/%s: %d, %v; want %d, %v", c.start, c.lab, got, ok, c.want, c.ok)
		}
	}
	// A meta.yaml that is a link is not read.
	if err := os.Symlink(filepath.Join(root, "work/hello/meta.yaml"), filepath.Join(root, "work/hello/checks/meta.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "x/lien"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "work/hello/meta.yaml"), filepath.Join(root, "x/lien/meta.yaml")); err != nil {
		t.Fatal(err)
	}
	if v, ok := InstalledVersion(filepath.Join(root, "x/lien"), "lien"); ok {
		t.Errorf("link read: %d", v)
	}
}

func TestStoreAtomicPrivateAndFound(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := &Page{HTML: []byte("<html>énoncé</html>"), Text: []byte("énoncé\n")}
	h, tx, err := Store("hello", 3, p)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := CacheDir()
	if h != filepath.Join(dir, "hello-v3.html") || tx != filepath.Join(dir, "hello-v3.txt") {
		t.Errorf("paths %s, %s", h, tx)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{h: 0o600, tx: 0o600, dir: 0o700} {
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
				t.Errorf("%s: %v, %v", path, info, err)
			}
		}
	}
	// A second write replaces the first; nothing else stays in the directory.
	if _, _, err := Store("hello", 3, &Page{HTML: []byte("v2"), Text: []byte("v2")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(h); string(got) != "v2" {
		t.Errorf("not replaced: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("entries %v", entries)
	}
	// Found by version, or the highest one.
	if _, _, err := Store("hello", 10, p); err != nil {
		t.Fatal(err)
	}
	if v, _, _, ok := Local("hello", 0); !ok || v != 10 {
		t.Errorf("highest = %d, %v", v, ok)
	}
	if v, _, _, ok := Local("hello", 3); !ok || v != 3 {
		t.Errorf("v3 = %d, %v", v, ok)
	}
	if _, _, _, ok := Local("autre", 0); ok {
		t.Error("copy of another lab")
	}
	// A copy that is a link is not used.
	if err := os.Remove(h); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", h); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := Local("hello", 3); ok {
		t.Error("link used as a copy")
	}
}

// A cache directory that already exists with wide rights is closed to 0700.
// Purge deletes the offline copies of every lab; nothing to delete is fine.
func TestPurgeDeletesEveryCopy(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Purge(); err != nil {
		t.Fatalf("Purge of nothing: %v", err)
	}
	for _, lab := range []string{"hello", "autre"} {
		if _, _, err := Store(lab, 1, &Page{HTML: []byte("<p>x</p>"), Text: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Purge(); err != nil {
		t.Fatal(err)
	}
	for _, lab := range []string{"hello", "autre"} {
		if _, _, _, ok := Local(lab, 0); ok {
			t.Errorf("copy of %s kept", lab)
		}
	}
}

func TestStoreClosesAnExistingDirectory(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, _ := CacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Store("hello", 1, &Page{HTML: []byte("x"), Text: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}

func TestStoreRefusesInvalidNames(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, lab := range []string{"../x", "a/b", "", "A"} {
		if _, _, err := Store(lab, 1, &Page{}); err == nil {
			t.Errorf("%q written", lab)
		}
	}
	if _, _, err := Store("hello", 0, &Page{}); err == nil {
		t.Error("version 0 written")
	}
}

func TestCacheDirIsUnderTheCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/var/cache/awa")
	if d, err := CacheDir(); err != nil || d != "/var/cache/awa/sagnac/instructions" {
		t.Errorf("CacheDir = %q, %v", d, err)
	}
}

func TestSanitize(t *testing.T) {
	in := "titre\x1b[31m rouge\x07\r\nligne\ttab\u009b\x7f é\n"
	if got := Sanitize(in); got != "titre[31m rouge\nligne\ttab é\n" {
		t.Errorf("%q", got)
	}
}

func TestIsModule(t *testing.T) {
	for arg, want := range map[string]bool{"B1-M1": true, "B12-M3": true, "hello": false, "b1-m1": false, "B1-M": false, "B1-M1x": false, "xB1-M1": false} {
		if IsModule(arg) != want {
			t.Errorf("IsModule(%q) = %v", arg, !want)
		}
	}
}

// Tests of the opener replace the system, the search in PATH and the launch.
type launch struct {
	name string
	args []string
}

func fakeSystem(t *testing.T, system string, onPath ...string) *[]launch {
	t.Helper()
	oldOS, oldLook, oldRun, oldOut := goos, lookPath, runCommand, outputCommand
	t.Cleanup(func() { goos, lookPath, runCommand, outputCommand = oldOS, oldLook, oldRun, oldOut })
	goos = system
	lookPath = func(name string) (string, error) {
		for _, n := range onPath {
			if n == name {
				return "/fake/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
	var runs []launch
	runCommand = func(_ context.Context, name string, args ...string) error {
		runs = append(runs, launch{name, args})
		return nil
	}
	outputCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no wslpath") }
	return &runs
}

func TestOpenerPerSystem(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "")
	for system, program := range map[string]string{"darwin": "open", "linux": "xdg-open"} {
		runs := fakeSystem(t, system, "open", "xdg-open", "wslview")
		ok, err := Open(context.Background(), "/tmp/x.html")
		if !ok || err != nil || len(*runs) != 1 || (*runs)[0].name != "/fake/"+program || strings.Join((*runs)[0].args, "|") != "/tmp/x.html" {
			t.Errorf("%s: %v %v %v", system, ok, err, *runs)
		}
	}
	// No program: nothing is started, the caller shows the path.
	runs := fakeSystem(t, "linux")
	if ok, err := Open(context.Background(), "/tmp/x.html"); ok || err != nil || len(*runs) != 0 {
		t.Errorf("none: %v %v %v", ok, err, *runs)
	}
	fakeSystem(t, "freebsd", "xdg-open")
	if ok, _ := Open(context.Background(), "/tmp/x.html"); ok {
		t.Error("unknown system opened")
	}
}

func TestOpenerWSL(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	runs := fakeSystem(t, "linux", "wslview", "explorer.exe", "xdg-open")
	if ok, err := Open(context.Background(), "/home/awa/.cache/x.html"); !ok || err != nil || (*runs)[0].name != "/fake/wslview" {
		t.Errorf("wslview: %v %v %v", ok, err, *runs)
	}
	// wslview missing from PATH: explorer.exe, never xdg-open.
	runs = fakeSystem(t, "linux", "explorer.exe", "xdg-open")
	if ok, err := Open(context.Background(), "/home/awa/.cache/x.html"); !ok || err != nil || (*runs)[0].name != "/fake/explorer.exe" {
		t.Errorf("explorer.exe: %v %v %v", ok, err, *runs)
	}
	// explorer.exe gets the Windows form of the path when wslpath gives it.
	runs = fakeSystem(t, "linux", "explorer.exe", "wslpath")
	outputCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "wslpath") && strings.Join(args, " ") == "-w /home/awa/x.html" {
			return []byte("\\\\wsl$\\Ubuntu\\home\\awa\\x.html\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	if _, err := Open(context.Background(), "/home/awa/x.html"); err != nil || (*runs)[0].args[0] != `\\wsl$\Ubuntu\home\awa\x.html` {
		t.Errorf("windows path: %v %v", err, *runs)
	}
	// explorer.exe exits with 1 on success; another status is a failure.
	fakeSystem(t, "linux", "explorer.exe")
	runCommand = func(ctx context.Context, name string, args ...string) error { return exitWith(t, 2) }
	if ok, err := Open(context.Background(), "/home/awa/x.html"); ok || err == nil {
		t.Errorf("explorer.exe exit 2: %v %v", ok, err)
	}
	// Exit 1 of another program is a failure too.
	fakeSystem(t, "darwin", "open")
	runCommand = func(ctx context.Context, name string, args ...string) error { return exec1(t) }
	if ok, err := Open(context.Background(), "/x.html"); ok || err == nil {
		t.Errorf("open exit 1: %v %v", ok, err)
	}
	fakeSystem(t, "linux", "explorer.exe")
	runCommand = func(ctx context.Context, name string, args ...string) error {
		return exec1(t)
	}
	if ok, err := Open(context.Background(), "/home/awa/x.html"); !ok || err != nil {
		t.Errorf("explorer.exe exit 1: %v %v", ok, err)
	}
	// Neither: the path is shown.
	runs = fakeSystem(t, "linux", "xdg-open")
	if ok, _ := Open(context.Background(), "/x.html"); ok || len(*runs) != 0 {
		t.Errorf("WSL without wslview or explorer.exe: %v", *runs)
	}
}

func TestOpenRefusesAnOptionAsTarget(t *testing.T) {
	runs := fakeSystem(t, "darwin", "open")
	for _, target := range []string{"", "-a", "--help"} {
		if ok, err := Open(context.Background(), target); ok || err == nil {
			t.Errorf("%q opened", target)
		}
	}
	if len(*runs) != 0 {
		t.Errorf("started: %v", *runs)
	}
}
