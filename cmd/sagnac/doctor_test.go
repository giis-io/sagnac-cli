package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/api"
)

// versionServer serves body as the version file and records what it receives.
type versionServer struct {
	URL    string
	hits   atomic.Int32
	header atomic.Value // http.Header of the last request
	status atomic.Int32
	body   atomic.Value // string
}

func newVersionServer(t *testing.T, body string) *versionServer {
	t.Helper()
	vs := &versionServer{}
	vs.body.Store(body)
	vs.status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vs.hits.Add(1)
		vs.header.Store(r.Header.Clone())
		if s := int(vs.status.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(vs.body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	vs.URL = srv.URL + "/cli/version.json"
	return vs
}

// isolate gives the test its own directories, the server's address and a fixed clock.
func isolate(t *testing.T, vs *versionServer, at time.Time) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("SAGNAC_VERSION_URL", vs.URL)
	t.Setenv("SAGNAC_API", "http://127.0.0.1:1")
	old := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = old })
}

var day = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

const securityFile = `{"latest":"1.2.4","minimum":"1.0.0","security":true,"advisory":"SAGNAC-2026-001"}`

func fileOf(latest string) string {
	return `{"latest":"` + latest + `","minimum":"1.0.0","security":false,"advisory":null}`
}

func TestNewerVersion(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.3.0"))
	isolate(t, vs, day)
	code, stdout, _ := execute(t, "doctor")
	if code != exitFailure {
		t.Errorf("exit code = %d, want 1 (a point to fix)", code)
	}
	if !strings.Contains(stdout, "1.3.0") || !strings.Contains(stdout, "1.2.3") || !strings.Contains(stdout, "À corriger") {
		t.Errorf("doctor does not report the newer version:\n%s", stdout)
	}
	// Same number with other build metadata, or a newer local build: up to date.
	for _, latest := range []string{"1.2.3", "1.2.2", "1.0.0"} {
		vs.body.Store(fileOf(latest))
		isolate(t, vs, day)
		code, stdout, _ = execute(t, "doctor")
		if code != exitOK || !strings.Contains(stdout, "à jour") {
			t.Errorf("latest %s: code %d, output:\n%s", latest, code, stdout)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0}, {"1.2.3+abc-dirty", "1.2.3", 0}, {"v1.2.3", "1.2.3", 0},
		{"1.2.3", "1.10.0", -1}, {"2.0.0", "1.99.99", 1}, {"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc.1", 1}, {"1.2.3-rc.1", "1.2.3-rc.2", 0}, {"0.1.0+27f6614", "0.1.1", -1},
	} {
		got, ok := api.Compare(c.a, c.b)
		if !ok || got != c.want {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d", c.a, c.b, got, ok, c.want)
		}
	}
	for _, bad := range []string{"dev", "", "1.2", "1.2.x", "1.2.3\n"} {
		if _, ok := api.Compare(bad, "1.2.3"); ok {
			t.Errorf("Compare(%q) accepted", bad)
		}
	}
}

// commandsWithWarnings are launched with arguments that make them stop early
// (no lab, no account): the warning must come before, and the exit code must
// be the one the command gives without the warning.
var commandsWithWarnings = [][]string{
	{"version"}, {"help"}, {"purge"}, {"logout"}, {"login"}, {"pull", "bonjour"}, {"open", "bonjour"}, {"check"},
}

// launchWarm runs args after a first launch that fills the cache when args read
// the cache only (help): the warnings then come from that copy.
func launchWarm(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	if len(args) == 0 || !networkCommands[args[0]] {
		execute(t, "purge")
	}
	return execute(t, args...)
}

func TestSecurityWarningOnAllCommands(t *testing.T) {
	vs := newVersionServer(t, securityFile)
	for _, args := range commandsWithWarnings {
		isolate(t, vs, day)
		code, _, stderr := launchWarm(t, args...)
		if !strings.Contains(stderr, "SAGNAC-2026-001") || !strings.Contains(stderr, "1.2.4") {
			t.Errorf("%v: no security warning on stderr:\n%s", args, stderr)
		}
		// Same command with a file that has no advisory: same exit code.
		quiet := newVersionServer(t, fileOf("1.2.3"))
		isolate(t, quiet, day)
		want, _, quietErr := launchWarm(t, args...)
		if strings.Contains(quietErr, "SAGNAC-") {
			t.Errorf("%v: warning without advisory:\n%s", args, quietErr)
		}
		if code != want {
			t.Errorf("%v: exit code %d with the warning, %d without", args, code, want)
		}
	}
	// Running the latest version: nothing to warn about.
	vs.body.Store(`{"latest":"1.2.3","minimum":"1.0.0","security":true,"advisory":"SAGNAC-2026-001"}`)
	isolate(t, vs, day)
	if _, _, stderr := execute(t, "purge"); strings.Contains(stderr, "SAGNAC-2026-001") {
		t.Errorf("warning for a version that already is the latest:\n%s", stderr)
	}
}

func TestServerTextIsNeverShown(t *testing.T) {
	for _, bad := range []string{
		`{"latest":"1.2.4","minimum":"1.0.0","security":true,"advisory":"\u001b[31mALERTE\u001b[0m"}`,
		`{"latest":"1.2.4","minimum":"1.0.0","security":true,"advisory":"a b"}`,
		`{"latest":"1.2.4","minimum":"1.0.0","security":true,"advisory":""}`,
		`{"latest":"\u001b]0;x\u0007","minimum":"1.0.0","security":false,"advisory":null}`,
		`{"latest":"1.2.4","minimum":"1.0.0","security":"yes","advisory":"SAGNAC-2026-001"}`,
		`<html>erreur</html>`,
	} {
		vs := newVersionServer(t, bad)
		isolate(t, vs, day)
		for _, args := range [][]string{{"purge"}, {"doctor"}} {
			_, stdout, stderr := execute(t, args...)
			for _, out := range []string{stdout, stderr} {
				if strings.ContainsAny(out, "\x1b\x07") || strings.Contains(out, "ALERTE") || strings.Contains(out, "html") {
					t.Errorf("%v: server text reached the terminal for %q:\n%q", args, bad, out)
				}
			}
		}
	}
}

func TestEndOfSupportSixMonths(t *testing.T) {
	old := endOfSupport
	t.Cleanup(func() { endOfSupport = old })
	for _, c := range []struct {
		end, now string
		want     string // "", "soon" or "ended"
	}{
		{"2032-12", "2026-10-19", ""},
		{"2032-12", "2032-06-30", ""},
		{"2032-12", "2032-07-01", "soon"},
		{"2032-12", "2032-12-31", "soon"},
		{"2032-12", "2033-01-01", "ended"},
		{"2027-02", "2026-08-31", ""},
		{"2027-02", "2026-09-01", "soon"},
		{"bientot", "2030-01-01", ""},
	} {
		at, _ := time.Parse("2006-01-02", c.now)
		endOfSupport = c.end
		kind, month := supportState(at)
		if kind != c.want {
			t.Errorf("end %s, now %s: state %q, want %q", c.end, c.now, kind, c.want)
		}
		if c.want != "" && month != c.end {
			t.Errorf("month = %q, want %q", month, c.end)
		}
	}
}

func TestEndOfSupportOnAllCommands(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	for _, args := range commandsWithWarnings {
		isolate(t, vs, time.Date(2032, 8, 1, 0, 0, 0, 0, time.UTC))
		code, stdout, stderr := launchWarm(t, args...)
		if !strings.Contains(stderr+stdout, "2032-12") || !strings.Contains(stderr+stdout, "se termine") {
			t.Errorf("%v: no end-of-support announcement:\n%s%s", args, stdout, stderr)
		}
		isolate(t, vs, day)
		if want, _, _ := launchWarm(t, args...); code != want {
			t.Errorf("%v: exit code %d with the announcement, %d without", args, code, want)
		}
		isolate(t, vs, time.Date(2033, 3, 1, 0, 0, 0, 0, time.UTC))
		if _, stdout, stderr := launchWarm(t, args...); !strings.Contains(stderr+stdout, "s'est terminé") {
			t.Errorf("%v: no announcement after the end:\n%s%s", args, stdout, stderr)
		}
	}
	// "sagnac version" shows it on stdout, once.
	isolate(t, vs, time.Date(2032, 8, 1, 0, 0, 0, 0, time.UTC))
	_, stdout, stderr := execute(t, "version")
	if !strings.Contains(stdout, "se termine") || strings.Contains(stderr, "se termine") {
		t.Errorf("version: stdout %q, stderr %q", stdout, stderr)
	}
}

func TestDoctorWithoutToken(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day)
	t.Setenv("SAGNAC_API", "http://127.0.0.1:1") // an API that would refuse anything
	code, stdout, stderr := execute(t, "doctor")
	if code != exitOK {
		t.Fatalf("exit code = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	h := vs.header.Load().(http.Header)
	for _, name := range []string{"Authorization", "Cookie"} {
		if h.Get(name) != "" {
			t.Errorf("%s sent to the version file: %q", name, h.Get(name))
		}
	}
	if ua := h.Get("User-Agent"); ua != "sagnac/1.2.3" {
		t.Errorf("User-Agent = %q, want only the version", ua)
	}
	if entries, _ := os.ReadDir(filepath.Join(os.Getenv("XDG_CONFIG_HOME"))); len(entries) != 0 {
		t.Errorf("doctor wrote in the configuration directory: %v", entries)
	}
	if !strings.Contains(stdout, "2032-12") {
		t.Errorf("doctor does not show the end of support:\n%s", stdout)
	}
}

func TestDoctorRefusesArguments(t *testing.T) {
	isolate(t, newVersionServer(t, fileOf("1.2.3")), day)
	if code, _, _ := execute(t, "doctor", "--share"); code != exitUsage {
		t.Errorf("exit code = %d, want 64", code)
	}
}

func TestVersionFileOncePerDay(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day)
	for i := 0; i < 3; i++ {
		execute(t, "purge")
		execute(t, "doctor")
	}
	if n := vs.hits.Load(); n != 1 {
		t.Fatalf("version file fetched %d times in a day, want 1", n)
	}
	clock = func() time.Time { return day.Add(25 * time.Hour) }
	execute(t, "purge")
	execute(t, "purge")
	if n := vs.hits.Load(); n != 2 {
		t.Errorf("version file fetched %d times after a day, want 2", n)
	}
	dir := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "sagnac")
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "version.json"): 0o600, filepath.Join(dir, "version.lock"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want mode %o", path, fi, err, want)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("cache directory holds %d entries, want version.json and its lock", len(entries))
	}
}

func TestVersionFileFailure(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	vs.status.Store(http.StatusInternalServerError)
	isolate(t, vs, day)
	code, _, stderr := execute(t, "purge")
	if code != exitOK || stderr != "" {
		t.Errorf("a failing version file changed purge: code %d, stderr %q", code, stderr)
	}
	execute(t, "purge")
	if n := vs.hits.Load(); n != 1 {
		t.Errorf("fetched %d times after a failure within the hour, want 1", n)
	}
	code, stdout, _ := execute(t, "doctor")
	if code != exitOK || !strings.Contains(stdout, "impossible de vérifier") {
		t.Errorf("doctor offline: code %d, output:\n%s", code, stdout)
	}
	// A failure keeps the last good copy.
	vs.status.Store(http.StatusOK)
	vs.body.Store(securityFile)
	clock = func() time.Time { return day.Add(26 * time.Hour) }
	execute(t, "purge")
	vs.status.Store(http.StatusInternalServerError)
	clock = func() time.Time { return day.Add(60 * time.Hour) }
	if _, _, stderr := execute(t, "purge"); !strings.Contains(stderr, "SAGNAC-2026-001") {
		t.Errorf("last good copy lost after a failure:\n%s", stderr)
	}
}

func TestVersionCheckTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	vs := &versionServer{URL: srv.URL + "/cli/version.json"}
	isolate(t, vs, day)
	old := api.VersionTimeout
	api.VersionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { api.VersionTimeout = old })
	start := time.Now()
	code, _, stderr := execute(t, "purge")
	if time.Since(start) > 2*time.Second || code != exitOK || stderr != "" {
		t.Errorf("slow version file: %v, code %d, stderr %q", time.Since(start), code, stderr)
	}
}

func TestVersionURLInsecure(t *testing.T) {
	isolate(t, newVersionServer(t, fileOf("1.2.3")), day)
	t.Setenv("SAGNAC_VERSION_URL", "http://exemple.invalid/cli/version.json")
	if code, _, stderr := execute(t, "purge"); code != exitOK || stderr != "" {
		t.Errorf("purge: code %d, stderr %q", code, stderr)
	}
	code, _, stderr := execute(t, "doctor")
	if code == exitOK || !strings.Contains(stderr, "SAGNAC_VERSION_URL") {
		t.Errorf("doctor: code %d, stderr %q", code, stderr)
	}
}

// help, a missing command and an unknown command answer at once: they read
// the cache of the version file and never open a connection. Nor does
// "-h" given to another command.
func TestHelpAndUnknownCommandsNeverUseTheNetwork(t *testing.T) {
	vs := newVersionServer(t, securityFile)
	cacheOnly := [][]string{{}, {"help"}, {"-h"}, {"--help"}, {"check", "-h"}, {"frobnicate"}}
	for _, args := range cacheOnly {
		isolate(t, vs, day)
		before := vs.hits.Load()
		start := time.Now()
		execute(t, args...)
		if n := vs.hits.Load() - before; n != 0 || time.Since(start) > 2*time.Second {
			t.Errorf("%v: %d request(s) to the version file, %v", args, n, time.Since(start))
		}
	}
	// With a cache, the same launches show the warnings, still without a request.
	isolate(t, vs, day)
	execute(t, "purge")
	hits := vs.hits.Load()
	for _, args := range cacheOnly {
		code, _, stderr := execute(t, args...)
		if !strings.Contains(stderr, "SAGNAC-2026-001") {
			t.Errorf("%v: no security warning from the cache:\n%s", args, stderr)
		}
		wantCode := exitOK
		if len(args) == 1 && args[0] == "frobnicate" {
			wantCode = exitUsage
		}
		if code != wantCode {
			t.Errorf("%v: exit code %d, want %d", args, code, wantCode)
		}
	}
	if n := vs.hits.Load(); n != hits {
		t.Errorf("cache-only launches made %d request(s)", n-hits)
	}
}

func TestEndOfSupportOnHelpAndUnknownCommands(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	for _, args := range [][]string{{}, {"-h"}, {"frobnicate"}} {
		isolate(t, vs, time.Date(2032, 8, 1, 0, 0, 0, 0, time.UTC))
		if _, _, stderr := execute(t, args...); !strings.Contains(stderr, "se termine") {
			t.Errorf("%v: no end-of-support announcement:\n%s", args, stderr)
		}
	}
}
