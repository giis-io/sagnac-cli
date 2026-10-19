package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

type member struct {
	name, body, link string
	typ              byte
	mode             int64
}

func kitArchive(t *testing.T, lab, cadre string, extra ...member) []byte {
	t.Helper()
	members := []member{
		{name: lab, typ: tar.TypeDir, mode: 0o755},
		{name: lab + "/Makefile", body: "include ../_cadre/lab.mk\n", typ: tar.TypeReg, mode: 0o644},
		{name: lab + "/checks/verify_a.sh", body: "#!/bin/sh\n", typ: tar.TypeReg, mode: 0o755},
		{name: "_cadre", typ: tar.TypeDir, mode: 0o755},
		{name: "_cadre/lab.mk", body: cadre, typ: tar.TypeReg, mode: 0o644},
		{name: "_cadre/bin/sagnac-tools", body: "#!/bin/sh\n", typ: tar.TypeReg, mode: 0o755},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range append(members, extra...) {
		h := &tar.Header{Name: m.name, Typeflag: m.typ, Mode: m.mode, Linkname: m.link, Size: int64(len(m.body)), Format: tar.FormatPAX}
		if m.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// kitServer serves /v1/kits/: the current version, then the archive.
type kitServer struct {
	mu         sync.Mutex
	paths      []string
	auth       []string
	archives   map[string][]byte // by lab
	version    int
	headerSum  string // "" : the digest of the archive
	currentSum string // "" : the digest of the archive
	status     int    // when not 0, every kit route answers status and body
	body       string
	invalid    int // the next requests answer 401 INVALID_TOKEN
	agents     []string
}

func (k *kitServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.paths = append(k.paths, r.URL.Path)
	k.auth = append(k.auth, r.Header.Get("Authorization"))
	k.agents = append(k.agents, r.Header.Get("User-Agent"))
	if k.invalid > 0 {
		k.invalid--
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code": "INVALID_TOKEN"}`)
		return
	}
	if k.status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(k.status)
		fmt.Fprint(w, k.body)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/kits/"), "/")
	archive, ok := k.archives[parts[0]]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"code": "UNKNOWN_LAB"}`)
		return
	}
	pick := func(s string) string {
		if s == "" {
			return digest(archive)
		}
		return s
	}
	if len(parts) == 1 {
		fmt.Fprintf(w, `{"lab": %q, "version": %d, "kit_sha256": %q}`, parts[0], k.version, pick(k.currentSum))
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Sagnac-Kit-Sha256", pick(k.headerSum))
	w.Write(archive)
}

func (k *kitServer) requested() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.paths...)
}

type pullRig struct {
	*rig
	kits   *kitServer
	api    string
	parent string
	work   string
}

// newPullRig starts the platform (device flow and kits), logs in, and moves
// into an empty directory work, inside parent.
func newPullRig(t *testing.T) *pullRig {
	t.Helper()
	r := newRig(t, "200")
	p := &pullRig{rig: r, kits: &kitServer{version: 3, archives: map[string][]byte{
		"hello": kitArchive(t, "hello", "setup:\n"),
	}}}
	mux := http.NewServeMux()
	mux.Handle("/v1/kits/", p.kits)
	mux.Handle("/", r.dev.handler(&p.api))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.api = srv.URL
	t.Setenv("SAGNAC_API", srv.URL)
	if err := r.store.Save(&account.Tokens{API: srv.URL, Access: testAccess, Refresh: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour), Scope: "kits:read"}); err != nil {
		t.Fatal(err)
	}
	p.parent = t.TempDir()
	p.work = filepath.Join(p.parent, "work")
	if err := os.Mkdir(p.work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(p.work)
	return p
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if p != root {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// nothingWritten: the parent holds only the empty work directory.
func (p *pullRig) nothingWritten(t *testing.T) {
	t.Helper()
	if got := tree(t, p.parent); len(got) != 1 || got[0] != "work" {
		t.Errorf("written: %v", got)
	}
}

func TestCurrentVersionRequested(t *testing.T) {
	p := newPullRig(t)
	code, stdout, stderr := p.run(t, "pull", "hello")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if got := p.kits.requested(); fmt.Sprint(got) != "[/v1/kits/hello /v1/kits/hello/3]" {
		t.Errorf("requests = %v", got)
	}
	for _, a := range p.kits.auth {
		if a != "Bearer "+testAccess {
			t.Errorf("Authorization = %q", a)
		}
	}
	for _, a := range p.kits.agents {
		if a != "sagnac/"+version {
			t.Errorf("User-Agent = %q, want sagnac/%s", a, version)
		}
	}
	if b, err := os.ReadFile(filepath.Join(p.work, "hello", "Makefile")); err != nil || len(b) == 0 {
		t.Errorf("hello/Makefile: %v", err)
	}
	if info, err := os.Stat(filepath.Join(p.work, "_cadre", "bin", "sagnac-tools")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("sagnac-tools not executable: %v", err)
	}
	want := i18n.T("pull.done", "hello", 3, filepath.Join(p.work, "hello"))
	if !strings.Contains(stdout, want) || !strings.Contains(stdout, "make setup") {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if strings.Contains(stdout+stderr, testAccess) {
		t.Error("token printed")
	}
}

func TestPullGivenVersion(t *testing.T) {
	p := newPullRig(t)
	code, _, stderr := p.run(t, "pull", "hello", "--version", "2")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if got := p.kits.requested(); got[len(got)-1] != "/v1/kits/hello/2" {
		t.Errorf("requests = %v", got)
	}
}

func TestWrongChecksumRejected(t *testing.T) {
	for _, field := range []string{"header", "current"} {
		p := newPullRig(t)
		if field == "header" {
			p.kits.headerSum = strings.Repeat("0", 64)
		} else {
			p.kits.currentSum = strings.Repeat("1", 64)
		}
		code, _, stderr := p.run(t, "pull", "hello")
		if code != exitInternal || !strings.Contains(stderr, i18n.T("pull.checksum")) {
			t.Errorf("%s: code %d, stderr %q", field, code, stderr)
		}
		p.nothingWritten(t)
	}
}

// A kit with ../ or with an outgoing link writes nothing.
func TestEscapingPathRejected(t *testing.T) {
	for _, name := range []string{"../evil", "hello/../../evil", "/tmp/sagnac-evil"} {
		p := newPullRig(t)
		p.kits.archives["hello"] = kitArchive(t, "hello", "setup:\n", member{name: name, body: "x", typ: tar.TypeReg, mode: 0o644})
		code, _, stderr := p.run(t, "pull", "hello")
		if code != exitInternal || !strings.Contains(stderr, i18n.T("pull.unsafe_kit", securityContactAddr)) {
			t.Errorf("%s: code %d, stderr %q", name, code, stderr)
		}
		p.nothingWritten(t)
	}
	if _, err := os.Lstat("/tmp/sagnac-evil"); err == nil {
		t.Error("/tmp/sagnac-evil written")
	}
}

func TestEscapingSymlinkRejected(t *testing.T) {
	p := newPullRig(t)
	p.kits.archives["hello"] = kitArchive(t, "hello", "setup:\n",
		member{name: "hello/out", typ: tar.TypeSymlink, link: "../.."},
		member{name: "hello/out/evil", body: "x", typ: tar.TypeReg, mode: 0o644})
	code, _, stderr := p.run(t, "pull", "hello")
	if code != exitInternal || !strings.Contains(stderr, i18n.T("pull.unsafe_kit", securityContactAddr)) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	p.nothingWritten(t)
}

func TestExistingDirectory(t *testing.T) {
	p := newPullRig(t)
	if err := os.Mkdir(filepath.Join(p.work, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := p.run(t, "pull", "hello")
	if code != exitFailure || !strings.Contains(stderr, i18n.T("pull.exists", "hello")) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if got := p.kits.requested(); len(got) != 0 {
		t.Errorf("requests sent: %v", got)
	}
}

func TestIdenticalFrameworkReused(t *testing.T) {
	p := newPullRig(t)
	p.kits.archives["autre"] = kitArchive(t, "autre", "setup:\n")
	if code, _, stderr := p.run(t, "pull", "hello"); code != exitOK {
		t.Fatalf("hello: %d %q", code, stderr)
	}
	code, stdout, stderr := p.run(t, "pull", "autre")
	if code != exitOK || !strings.Contains(stdout, i18n.T("pull.framework_reused")) {
		t.Fatalf("autre: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(p.work, "autre", "Makefile")); err != nil {
		t.Error(err)
	}
}

func TestDifferentFrameworkRejected(t *testing.T) {
	p := newPullRig(t)
	p.kits.archives["autre"] = kitArchive(t, "autre", "setup: changed\n")
	if code, _, stderr := p.run(t, "pull", "hello"); code != exitOK {
		t.Fatalf("hello: %d %q", code, stderr)
	}
	code, _, stderr := p.run(t, "pull", "autre")
	if code != exitFailure || !strings.Contains(stderr, i18n.T("pull.framework_differs", "autre")) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if _, err := os.Lstat(filepath.Join(p.work, "autre")); !os.IsNotExist(err) {
		t.Errorf("autre written: %v", err)
	}
	if got := tree(t, p.work); len(got) == 0 || strings.Contains(fmt.Sprint(got), ".sagnac-") {
		t.Errorf("work = %v", got)
	}

	// A _cadre/ without its fingerprint is refused before any request.
	q := newPullRig(t)
	if err := os.Mkdir(filepath.Join(q.work, "_cadre"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = q.run(t, "pull", "hello")
	if code != exitFailure || !strings.Contains(stderr, i18n.T("pull.framework_differs", "hello")) || len(q.kits.requested()) != 0 {
		t.Errorf("no .sha256: code %d, stderr %q, requests %v", code, stderr, q.kits.requested())
	}
}

func TestEntitlementInactiveMessage(t *testing.T) {
	for state, key := range map[string]string{
		`"unpaid"`: "pull.entitlement_unpaid", `"cancelled"`: "pull.entitlement_cancelled",
		`"expired"`: "pull.entitlement_expired", `null`: "pull.entitlement_inactive",
	} {
		p := newPullRig(t)
		p.kits.status, p.kits.body = 403, `{"code": "ENTITLEMENT_INACTIVE", "state": `+state+`}`
		code, stdout, stderr := p.run(t, "pull", "hello")
		if code != exitFailure || !strings.Contains(stderr, i18n.T(key)) || stdout != "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", state, code, stdout, stderr)
		}
		p.nothingWritten(t)
	}
}

func TestPullOtherServerCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
		code   int
	}{
		{404, `{"code": "UNKNOWN_LAB"}`, i18n.T("pull.unknown_lab", "hello"), exitFailure},
		{403, `{"code": "INSUFFICIENT_SCOPE"}`, i18n.T("pull.insufficient_scope"), exitFailure},
		{401, `{"code": "INVALID_TOKEN"}`, i18n.T("error.login_required"), exitFailure},
		{502, `<html>proxy says hello</html>`, i18n.T("error.unexpected_response"), exitEnvironment},
	}
	for _, c := range cases {
		p := newPullRig(t)
		p.kits.status, p.kits.body = c.status, c.body
		code, _, stderr := p.run(t, "pull", "hello")
		if code != c.code || !strings.Contains(stderr, c.want) || strings.Contains(stderr, "proxy") {
			t.Errorf("%d: code %d, stderr %q", c.status, code, stderr)
		}
		p.nothingWritten(t)
	}
}

// With --session the family is revoked at the end, success or failure, and
// the stored login is left as it was.
func TestPullSessionReleasedOnEveryPath(t *testing.T) {
	cases := map[string]func(p *pullRig){
		"success": func(p *pullRig) {},
		"entitlement": func(p *pullRig) {
			p.kits.status, p.kits.body = 403, `{"code": "ENTITLEMENT_INACTIVE", "state": "expired"}`
		},
		"checksum": func(p *pullRig) { p.kits.headerSum = strings.Repeat("0", 64) },
		"unsafe": func(p *pullRig) {
			p.kits.archives["hello"] = kitArchive(t, "hello", "", member{name: "../evil", typ: tar.TypeReg, mode: 0o644})
		},
	}
	for name, setup := range cases {
		p := newPullRig(t)
		setup(p)
		code, _, stderr := p.run(t, "pull", "--session", "hello")
		if (name == "success") != (code == exitOK) {
			t.Errorf("%s: code %d, %q", name, code, stderr)
		}
		if len(p.dev.codeForms) != 1 || len(p.dev.revokeForms) != 1 || p.dev.revokeForms[0].Get("token") != testRefresh {
			t.Errorf("%s: %d device flows, revocations %v", name, len(p.dev.codeForms), p.dev.revokeForms)
		}
		if got, _ := p.store.Load(); got == nil || got.Access != testAccess {
			t.Errorf("%s: stored login touched: %+v", name, got)
		}
		if len(p.session) != 1 || !p.session[0] {
			t.Errorf("%s: stores opened %v", name, p.session)
		}
	}
}

func TestPullArguments(t *testing.T) {
	p := newPullRig(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"pull"}, i18n.T("pull.lab_missing")},
		{[]string{"pull", "../x"}, i18n.T("pull.invalid_lab", "../x")},
		{[]string{"pull", "Hello"}, i18n.T("pull.invalid_lab", "Hello")},
		{[]string{"pull", "hello", "autre"}, i18n.T("error.unexpected_argument", "autre", "pull")},
		{[]string{"pull", "hello", "--version"}, i18n.T("pull.version_invalid")},
		{[]string{"pull", "hello", "--version", "x"}, i18n.T("pull.version_invalid")},
		{[]string{"pull", "hello", "--version=0"}, i18n.T("pull.version_invalid")},
		{[]string{"pull", "hello", "--version", "-3"}, i18n.T("pull.version_invalid")},
		{[]string{"pull", "hello", "--force"}, i18n.T("error.unknown_option", "--force")},
	}
	for _, c := range cases {
		code, _, stderr := p.run(t, c.args...)
		if code != exitUsage || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: code %d, stderr %q, want %q", c.args, code, stderr, c.want)
		}
	}
	if got := p.kits.requested(); len(got) != 0 || len(p.dev.codeForms) != 0 {
		t.Errorf("requests sent: %v", got)
	}
	p.nothingWritten(t)
}

// 401 INVALID_TOKEN: one forced refresh, the request replayed, the pull goes on.
func TestPullRefreshesAndReplaysOnInvalidToken(t *testing.T) {
	p := newPullRig(t)
	p.kits.invalid = 1
	code, _, stderr := p.run(t, "pull", "hello")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if got := p.kits.requested(); fmt.Sprint(got) != "[/v1/kits/hello /v1/kits/hello /v1/kits/hello/3]" {
		t.Errorf("requests = %v", got)
	}
	if len(p.dev.tokenForms) != 1 || p.dev.tokenForms[0].Get("grant_type") != "refresh_token" {
		t.Errorf("token requests %v", p.dev.tokenForms)
	}
}

// The family is closed: the message depends on the mode. With --session it
// never sends the person to "sagnac login".
func TestPullSessionEndedMessageDependsOnMode(t *testing.T) {
	for _, session := range []bool{false, true} {
		p := newPullRig(t)
		p.kits.invalid = 1
		p.dev.answers = []string{`{"error": "invalid_grant"}`}
		if session {
			p.dev.answers = []string{"200", `{"error": "invalid_grant"}`} // the device flow, then the refresh
		}
		args := []string{"pull", "hello"}
		if session {
			args = append(args, "--session")
		}
		code, _, stderr := p.run(t, args...)
		want := i18n.T("error.login_required")
		if session {
			want = i18n.T("error.session_ended")
			if strings.Contains(stderr, "sagnac login") {
				t.Errorf("session: advises sagnac login: %q", stderr)
			}
		}
		if code != exitFailure || !strings.Contains(stderr, want) {
			t.Errorf("session=%v: code %d, stderr %q, want %q", session, code, stderr, want)
		}
		p.nothingWritten(t)
	}
}

// An error of the directory other than "does not exist" is not "exists".
func TestPullUnreadableDirectoryHasItsOwnMessage(t *testing.T) {
	p := newPullRig(t)
	old := lstat
	lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrPermission }
	t.Cleanup(func() { lstat = old })
	code, _, stderr := p.run(t, "pull", "hello")
	if code != exitInternal || !strings.Contains(stderr, i18n.T("pull.stat_failed", "hello")) || strings.Contains(stderr, i18n.T("pull.exists", "hello")) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if got := p.kits.requested(); len(got) != 0 {
		t.Errorf("requests sent: %v", got)
	}
}
