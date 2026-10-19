package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// instructionsServer serves the routes of the instructions and the current
// version of a kit, like the platform.
type instructionsServer struct {
	mu          sync.Mutex
	requests    []string // RequestURI
	auth        []string
	current     map[string]int
	html, text  string
	modules     map[string][][2]any // module -> (lab, version)
	notIncluded bool
	pageURL     string // "url" of the 403; "" : the page of this server
	api         string
	badType     bool
}

func (s *instructionsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.URL.RequestURI())
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/kits/"):
		lab := strings.TrimPrefix(r.URL.Path, "/v1/kits/")
		v, ok := s.current[lab]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"code": "UNKNOWN_LAB"}`)
			return
		}
		fmt.Fprintf(w, `{"lab": %q, "version": %d, "kit_sha256": %q}`, lab, v, strings.Repeat("a", 64))
	case r.URL.Path == "/v1/instructions":
		labs, ok := s.modules[r.URL.Query().Get("module")]
		if !ok {
			labs = nil
		}
		w.Header().Set("Content-Type", "application/json")
		items := []string{}
		for _, l := range labs {
			items = append(items, fmt.Sprintf(`{"lab": %q, "version": %d}`, l[0], l[1]))
		}
		fmt.Fprintf(w, `{"labs": [%s]}`, strings.Join(items, ","))
	case strings.HasPrefix(r.URL.Path, "/v1/instructions/"):
		lab := strings.TrimPrefix(r.URL.Path, "/v1/instructions/")
		if _, ok := s.current[lab]; !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"code": "UNKNOWN_LAB"}`)
			return
		}
		if s.notIncluded {
			u := s.pageURL
			if u == "" {
				u = s.api + "/labs/" + lab + "/enonce?version=" + r.URL.Query().Get("version")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			enc, _ := json.Marshal(u)
			fmt.Fprintf(w, `{"code": "OFFLINE_INSTRUCTIONS_NOT_INCLUDED", "url": %s}`, enc)
			return
		}
		if r.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, s.text)
			return
		}
		if s.badType {
			w.Header().Set("Content-Type", "application/octet-stream")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		fmt.Fprint(w, s.html)
	default:
		w.WriteHeader(404)
	}
}

func (s *instructionsServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

type openRig struct {
	*rig
	ins      *instructionsServer
	srv      *httptest.Server
	cache    string
	work     string
	launched []string
	launchOK bool
}

func newOpenRig(t *testing.T) *openRig {
	t.Helper()
	r := newRig(t, "200")
	o := &openRig{rig: r, launchOK: true, ins: &instructionsServer{
		current: map[string]int{"hello": 3, "bonjour": 2},
		html:    `<html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'self'; img-src data:"></head><body>Énoncé de hello</body></html>`,
		text:    "Énoncé de hello\n1. Lancez make.\n",
		modules: map[string][][2]any{"B1-M1": {{"hello", 3}, {"bonjour", 2}}},
	}}
	mux := http.NewServeMux()
	mux.Handle("/v1/kits/", o.ins)
	mux.Handle("/v1/instructions", o.ins)
	mux.Handle("/v1/instructions/", o.ins)
	mux.Handle("/", r.dev.handler(&o.ins.api))
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)
	o.ins.api = o.srv.URL
	t.Setenv("SAGNAC_API", o.srv.URL)
	if err := r.store.Save(&account.Tokens{API: o.srv.URL, Access: testAccess, Refresh: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour), Scope: "instructions:read kits:read"}); err != nil {
		t.Fatal(err)
	}
	o.cache = t.TempDir()
	t.Setenv("XDG_CACHE_HOME", o.cache)
	o.work = t.TempDir()
	t.Chdir(o.work)
	old := launch
	launch = func(_ context.Context, target string) (bool, error) {
		o.launched = append(o.launched, target)
		return o.launchOK, nil
	}
	t.Cleanup(func() { launch = old })
	return o
}

func (o *openRig) copyPath(name string) string {
	return filepath.Join(o.cache, "sagnac", "instructions", name)
}

func TestLocalCopyWritten(t *testing.T) {
	o := newOpenRig(t)
	code, stdout, stderr := o.run(t, "open", "hello")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	html, err := os.ReadFile(o.copyPath("hello-v3.html"))
	if err != nil || string(html) != o.ins.html {
		t.Fatalf("html copy: %q, %v", html, err)
	}
	if txt, err := os.ReadFile(o.copyPath("hello-v3.txt")); err != nil || string(txt) != o.ins.text {
		t.Errorf("text copy: %q, %v", txt, err)
	}
	for _, f := range []string{"hello-v3.html", "hello-v3.txt"} {
		if info, _ := os.Stat(o.copyPath(f)); info == nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v", f, info)
		}
	}
	if info, _ := os.Stat(filepath.Dir(o.copyPath("x"))); info == nil || info.Mode().Perm() != 0o700 {
		t.Errorf("directory: %v", info)
	}
	if entries, _ := os.ReadDir(filepath.Dir(o.copyPath("x"))); len(entries) != 2 {
		t.Errorf("entries %v", entries)
	}
	if len(o.launched) != 1 || o.launched[0] != o.copyPath("hello-v3.html") {
		t.Errorf("opened %v", o.launched)
	}
	if want := i18n.T("open.opened", "hello", 3, o.copyPath("hello-v3.html")); !strings.Contains(stdout, want) {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if got := fmt.Sprint(o.ins.seen()); got != "[/v1/kits/hello /v1/instructions/hello?version=3 /v1/instructions/hello?format=text&version=3]" &&
		got != "[/v1/kits/hello /v1/instructions/hello?version=3 /v1/instructions/hello?version=3&format=text]" {
		t.Errorf("requests %s", got)
	}
}

func TestInstalledKitVersion(t *testing.T) {
	o := newOpenRig(t)
	if err := os.MkdirAll(filepath.Join(o.work, "hello", "checks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.work, "hello", "meta.yaml"), []byte("id: hello\nversion: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// From the kit's own subdirectory too: found by going up.
	t.Chdir(filepath.Join(o.work, "hello", "checks"))
	if code, _, stderr := o.run(t, "open", "hello"); code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	for _, r := range o.ins.seen() {
		if strings.HasPrefix(r, "/v1/kits/") {
			t.Errorf("current version asked although the kit is installed: %v", o.ins.seen())
		}
	}
	if _, err := os.Stat(o.copyPath("hello-v2.html")); err != nil {
		t.Errorf("installed version 2 not used: %v", err)
	}
	if got := o.ins.seen()[0]; got != "/v1/instructions/hello?version=2" {
		t.Errorf("first request %s", got)
	}
}

func TestNoOfflineEntitlementOpensURL(t *testing.T) {
	o := newOpenRig(t)
	o.ins.notIncluded = true
	code, stdout, stderr := o.run(t, "open", "hello")
	want := o.srv.URL + "/labs/hello/enonce?version=3"
	if code != exitOK || len(o.launched) != 1 || o.launched[0] != want {
		t.Fatalf("code %d, opened %v, stderr %q", code, o.launched, stderr)
	}
	if !strings.Contains(stdout, i18n.T("open.site_opened", "hello", want)) {
		t.Errorf("stdout %q", stdout)
	}
	if _, err := os.Stat(o.copyPath("hello-v3.html")); !os.IsNotExist(err) {
		t.Errorf("a copy was written: %v", err)
	}
	// Nothing to launch: the address is shown.
	o.launched, o.launchOK = nil, false
	code, stdout, _ = o.run(t, "open", "hello")
	if code != exitOK || !strings.Contains(stdout, i18n.T("open.site_shown", "hello", want)) {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	// --text shows the address instead of opening it.
	o.launched, o.launchOK = nil, true
	code, stdout, _ = o.run(t, "open", "hello", "--text")
	if code != exitOK || len(o.launched) != 0 || !strings.Contains(stdout, want) {
		t.Errorf("--text: code %d, opened %v, stdout %q", code, o.launched, stdout)
	}
}

// The address comes from the server: only a page of its own site is opened.
func TestForeignPageNeverOpened(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example/labs/hello/enonce", "javascript:alert(1)", "file:///etc/passwd", "-a Calculator",
		"http://127.0.0.1:1/labs/hello/enonce", "{API}/labs/hello/enonce?next=https://evil.example",
		"{API}/labs/autre/enonce", "{API}@evil.example/labs/hello/enonce", "{API}/labs/hello/enonce\x1b[2J",
	} {
		o := newOpenRig(t)
		o.ins.notIncluded = true
		o.ins.pageURL = strings.ReplaceAll(bad, "{API}", o.srv.URL)
		code, stdout, stderr := o.run(t, "open", "hello")
		if code != exitInternal || len(o.launched) != 0 || !strings.Contains(stderr, i18n.T("open.url_refused")) {
			t.Errorf("%q: code %d, opened %v, stderr %q", bad, code, o.launched, stderr)
		}
		if strings.Contains(stdout+stderr, "evil") || strings.Contains(stdout+stderr, "\x1b") {
			t.Errorf("%q: the server's address is shown: %q %q", bad, stdout, stderr)
		}
	}
}

func TestOfflineUsesCopy(t *testing.T) {
	o := newOpenRig(t)
	if code, _, stderr := o.run(t, "open", "hello"); code != exitOK {
		t.Fatalf("online: %d %q", code, stderr)
	}
	o.launched = nil
	o.srv.Close() // no network
	code, stdout, stderr := o.run(t, "open", "hello")
	if code != exitOK || len(o.launched) != 1 || o.launched[0] != o.copyPath("hello-v3.html") {
		t.Fatalf("code %d, opened %v, stderr %q", code, o.launched, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout), "hors ligne") ||
		!strings.Contains(stdout, i18n.T("open.offline_opened", "hello", 3, o.copyPath("hello-v3.html"))) {
		t.Errorf("stdout %q does not say it is offline", stdout)
	}
	// --text offline: the text of the copy, the note on stderr.
	code, stdout, stderr = o.run(t, "open", "hello", "--text")
	if code != exitOK || stdout != o.ins.text || !strings.Contains(strings.ToLower(stderr), "hors ligne") {
		t.Errorf("--text: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// No copy of this lab: the lab stays runnable, exit 1.
	code, stdout, stderr = o.run(t, "open", "bonjour")
	if code != exitEnvironment || stdout != "" || !strings.Contains(stderr, i18n.T("open.offline_no_copy", "bonjour")) ||
		!strings.Contains(stderr, "exécutable") {
		t.Errorf("no copy: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// A module needs the list from the server.
	code, _, stderr = o.run(t, "open", "B1-M1")
	if code != exitEnvironment || !strings.Contains(stderr, i18n.T("open.offline_module")) {
		t.Errorf("module: code %d, stderr %q", code, stderr)
	}
}

// Offline with an installed kit: the version of the kit picks the copy.
func TestOfflineUsesCopyOfInstalledVersion(t *testing.T) {
	o := newOpenRig(t)
	if code, _, stderr := o.run(t, "open", "hello"); code != exitOK {
		t.Fatalf("online: %d %q", code, stderr)
	}
	newer := o.copyPath("hello-v9.html")
	if err := os.WriteFile(newer, []byte("<html>v9</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(o.work, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.work, "hello", "meta.yaml"), []byte("id: hello\nversion: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.launched = nil
	o.srv.Close()
	if code, _, stderr := o.run(t, "open", "hello"); code != exitOK || o.launched[0] != o.copyPath("hello-v3.html") {
		t.Errorf("code %d, opened %v, stderr %q", code, o.launched, stderr)
	}
}

func TestNoTokenInURL(t *testing.T) {
	o := newOpenRig(t)
	if code, _, stderr := o.run(t, "open", "hello"); code != exitOK {
		t.Fatalf("%d %q", code, stderr)
	}
	if code, _, stderr := o.run(t, "open", "B1-M1"); code != exitOK {
		t.Fatalf("%d %q", code, stderr)
	}
	o.ins.notIncluded = true
	if code, _, stderr := o.run(t, "open", "bonjour"); code != exitOK {
		t.Fatalf("%d %q", code, stderr)
	}
	for _, r := range o.ins.seen() {
		if strings.Contains(r, testAccess) || strings.Contains(r, testRefresh) || strings.Contains(strings.ToLower(r), "token") {
			t.Errorf("token in an address: %s", r)
		}
	}
	for _, a := range o.ins.auth {
		if a != "Bearer "+testAccess {
			t.Errorf("Authorization %q", a)
		}
	}
	for _, l := range o.launched {
		if strings.Contains(l, testAccess) || strings.Contains(l, testRefresh) {
			t.Errorf("token in the opened target: %s", l)
		}
	}
}

func TestOpenModulePreloads(t *testing.T) {
	o := newOpenRig(t)
	o.ins.current["bonjour"] = 2
	code, stdout, stderr := o.run(t, "open", "B1-M1")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if len(o.launched) != 0 {
		t.Errorf("opened %v", o.launched)
	}
	for _, f := range []string{"hello-v3.html", "hello-v3.txt", "bonjour-v2.html", "bonjour-v2.txt"} {
		if _, err := os.Stat(o.copyPath(f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, want := range []string{i18n.T("open.module_done", "B1-M1", 2), o.copyPath("hello-v3.html"), o.copyPath("bonjour-v2.html")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if got := o.ins.seen()[0]; got != "/v1/instructions?module=B1-M1" {
		t.Errorf("first request %s", got)
	}
	// An unknown module, an offer without the offline copy.
	if code, _, stderr := o.run(t, "open", "B9-M9"); code != exitFailure || !strings.Contains(stderr, i18n.T("open.module_empty", "B9-M9")) {
		t.Errorf("empty: %d %q", code, stderr)
	}
	o.ins.notIncluded = true
	if code, _, stderr := o.run(t, "open", "B1-M1"); code != exitFailure || !strings.Contains(stderr, i18n.T("open.module_not_included")) {
		t.Errorf("not included: %d %q", code, stderr)
	}
}

func TestOpenText(t *testing.T) {
	o := newOpenRig(t)
	o.ins.text = "Énoncé\x1b]0;titre\x07 propre\n1. Lancez make.\n"
	code, stdout, stderr := o.run(t, "open", "hello", "--text")
	if code != exitOK || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if stdout != "Énoncé]0;titre propre\n1. Lancez make.\n" {
		t.Errorf("stdout %q", stdout)
	}
	if len(o.launched) != 0 {
		t.Errorf("opened %v", o.launched)
	}
	if _, err := os.Stat(o.copyPath("hello-v3.txt")); err != nil {
		t.Errorf("copy not written: %v", err)
	}
}

func TestOpenNothingToLaunchShowsThePath(t *testing.T) {
	o := newOpenRig(t)
	o.launchOK = false
	code, stdout, _ := o.run(t, "open", "hello")
	if code != exitOK || !strings.Contains(stdout, i18n.T("open.path_only", "hello", 3, o.copyPath("hello-v3.html"))) {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
}

func TestOpenServerRefusals(t *testing.T) {
	cases := []struct {
		name string
		want string
		code int
		set  func(o *openRig)
	}{
		{"unknown lab", i18n.T("open.unknown_target", "inconnu"), exitFailure, func(o *openRig) {}},
		{"wrong content type", i18n.T("error.unexpected_response"), exitInternal, func(o *openRig) { o.ins.badType = true }},
	}
	for _, c := range cases {
		o := newOpenRig(t)
		c.set(o)
		target := "hello"
		if c.name == "unknown lab" {
			target = "inconnu"
		}
		code, stdout, stderr := o.run(t, "open", target)
		if code != c.code || stdout != "" || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: code %d, stdout %q, stderr %q", c.name, code, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(o.cache, "sagnac", "instructions")); !os.IsNotExist(err) {
			t.Errorf("%s: cache created", c.name)
		}
	}
}

func TestOpenArguments(t *testing.T) {
	o := newOpenRig(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"open"}, i18n.T("open.target_missing")},
		{[]string{"open", "../x"}, i18n.T("open.invalid_target", "../x")},
		{[]string{"open", "Hello"}, i18n.T("open.invalid_target", "Hello")},
		{[]string{"open", "hello", "bonjour"}, i18n.T("error.unexpected_argument", "bonjour", "open")},
		{[]string{"open", "hello", "--force"}, i18n.T("error.unknown_option", "--force")},
		{[]string{"open", "B1-M1", "--text"}, i18n.T("open.text_module")},
	}
	for _, c := range cases {
		code, _, stderr := o.run(t, c.args...)
		if code != exitUsage || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: code %d, stderr %q, want %q", c.args, code, stderr, c.want)
		}
	}
	if got := o.ins.seen(); len(got) != 0 || len(o.dev.codeForms) != 0 {
		t.Errorf("requests sent: %v", got)
	}
}

// With --session the family is revoked at the end, whatever the outcome, and
// the stored login stays as it was.
func TestOpenSessionReleasedOnEveryPath(t *testing.T) {
	cases := map[string]func(o *openRig){
		"success":      func(o *openRig) {},
		"not included": func(o *openRig) { o.ins.notIncluded = true },
		"module":       func(o *openRig) {},
	}
	for name, setup := range cases {
		o := newOpenRig(t)
		setup(o)
		target := "hello"
		if name == "module" {
			target = "B1-M1"
		}
		code, _, stderr := o.run(t, "open", target, "--session")
		if code != exitOK {
			t.Errorf("%s: code %d, %q", name, code, stderr)
		}
		if len(o.dev.codeForms) != 1 || len(o.dev.revokeForms) != 1 || o.dev.revokeForms[0].Get("token") != testRefresh {
			t.Errorf("%s: %d device flows, revocations %v", name, len(o.dev.codeForms), o.dev.revokeForms)
		}
		if got, _ := o.store.Load(); got == nil || got.Access != testAccess {
			t.Errorf("%s: stored login touched: %+v", name, got)
		}
	}
	o := newOpenRig(t)
	o.ins.current = map[string]int{}
	if code, _, _ := o.run(t, "open", "hello", "--session"); code != exitFailure || len(o.dev.revokeForms) != 1 {
		t.Errorf("failure: code %d, revocations %v", code, o.dev.revokeForms)
	}
}

// 401 INVALID_TOKEN: one refresh, one replay; --session never advises login.
func TestOpenInvalidTokenReplayedAndSessionMessage(t *testing.T) {
	o := newOpenRig(t)
	first := true
	wrapped := http.NewServeMux()
	wrapped.HandleFunc("/v1/instructions/", func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			w.WriteHeader(401)
			fmt.Fprint(w, `{"code": "INVALID_TOKEN"}`)
			return
		}
		o.ins.ServeHTTP(w, r)
	})
	wrapped.Handle("/", o.srv.Config.Handler)
	o.srv.Config.Handler = wrapped
	code, _, stderr := o.run(t, "open", "hello")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if len(o.dev.tokenForms) != 1 || o.dev.tokenForms[0].Get("grant_type") != "refresh_token" {
		t.Errorf("token requests %v", o.dev.tokenForms)
	}

	// The family is closed: --session says to run the command again.
	o = newOpenRig(t)
	o.dev.answers = []string{"200", `{"error": "invalid_grant"}`}
	denied := http.NewServeMux()
	denied.HandleFunc("/v1/instructions/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"code": "INVALID_TOKEN"}`)
	})
	denied.Handle("/", o.srv.Config.Handler)
	o.srv.Config.Handler = denied
	code, _, stderr = o.run(t, "open", "hello", "--session")
	if code != exitFailure || !strings.Contains(stderr, i18n.T("error.session_ended")) || strings.Contains(stderr, "sagnac login") {
		t.Errorf("session: code %d, stderr %q", code, stderr)
	}
}

// The page is kept byte for byte: its Content-Security-Policy tag is what
// stops a script of the instructions when the file is opened from disk.
func TestCopyKeepsContentSecurityPolicy(t *testing.T) {
	o := newOpenRig(t)
	if code, _, stderr := o.run(t, "open", "hello"); code != exitOK {
		t.Fatalf("%d %q", code, stderr)
	}
	html, err := os.ReadFile(o.copyPath("hello-v3.html"))
	if err != nil || string(html) != o.ins.html || !strings.Contains(string(html), `http-equiv="Content-Security-Policy"`) {
		t.Errorf("copy %q, %v", html, err)
	}
}

// --text shows the text before the copy is written; a cache that cannot be
// written is a warning, not a failure.
func TestOpenTextShownEvenIfCacheCannotBeWritten(t *testing.T) {
	o := newOpenRig(t)
	blocker := filepath.Join(t.TempDir(), "fichier")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", blocker) // a file where the directory should be
	code, stdout, stderr := o.run(t, "open", "hello", "--text")
	if code != exitOK || stdout != o.ins.text || !strings.Contains(stderr, i18n.T("open.cache_warning")) {
		t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Without --text, the same cache is a failure.
	if code, _, stderr := o.run(t, "open", "hello"); code != exitInternal || !strings.Contains(stderr, i18n.T("open.cache_failed")) {
		t.Errorf("page: code %d, stderr %q", code, stderr)
	}
}

// With --session the scope message does not send the person to login.
func TestInsufficientScopeMessageDependsOnMode(t *testing.T) {
	for _, session := range []bool{false, true} {
		var b strings.Builder
		err := &api.APIError{Status: 403, Code: codeInsufficientScope}
		reportOpenError(&b, err, "hello", "http://x", session)
		reportKitAPIError(&b, err, "hello", session)
		out := b.String()
		if session && (strings.Contains(out, "sagnac login") || !strings.Contains(out, i18n.T("error.scope_session"))) {
			t.Errorf("session: %q", out)
		}
		if !session && !strings.Contains(out, "sagnac login") {
			t.Errorf("no session: %q", out)
		}
	}
}
