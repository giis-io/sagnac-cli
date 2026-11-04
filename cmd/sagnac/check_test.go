package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/instructions"
	"github.com/giis-io/sagnac-cli/internal/oracle"
	"github.com/giis-io/sagnac-cli/internal/queue"
	"github.com/giis-io/sagnac-cli/internal/testkit"
)

const (
	testSession = "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c"
	testSeed    = 777
	// testLogin is the connection of the tokens of the rig.
	testLogin = "3e6c4f0b-7d8a-4c5e-8f4b-5a6c7d8e9f0a"
)

var reUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// checkLib is a copy of the result functions of lib/controle.sh.
const checkLib = `_sagnac_ecrire() {
    if [ -n "$2" ]; then printf 'observed=%s\n' "$2" >>"$SAGNAC_RESULT"; fi
    if [ -n "$3" ]; then printf 'expected=%s\n' "$3" >>"$SAGNAC_RESULT"; fi
    if [ -n "$1" ]; then printf 'code=%s\n' "$1" >>"$SAGNAC_RESULT"; fi
}
sagnac_reussite() { _sagnac_ecrire "" "$1" "$2"; exit 0; }
sagnac_echec() { _sagnac_ecrire "$1" "$2" "$3"; exit 1; }
`

type sentAttempt struct {
	key   string
	auth  string
	agent string
	body  map[string]any
}

// platform serves /v1/sessions and /v1/attempts. An attempt is stored once
// per key, like the server; a status of attemptStatus is used in order (the
// last repeats): 0 is a success, -1 cuts the connection after reading.
type platform struct {
	mu            sync.Mutex
	sessionStatus int
	sessionBody   string
	attemptStatus []int
	attemptBody   string
	retryAfter    string // Retry-After of every refusal, when set
	sessions      []map[string]any
	sessionAgents []string
	attempts      []sentAttempt
	stored        map[string]bool
	onAttempt     func()                 // called without the lock when an attempt arrives
	headers       map[string]http.Header // headers of the last request, by path
}

func (p *platform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if r.URL.Path == "/v1/attempts" && p.onAttempt != nil {
		p.onAttempt()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if p.headers == nil {
		p.headers = map[string]http.Header{}
	}
	p.headers[r.URL.Path] = r.Header.Clone()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/sessions":
		p.sessions = append(p.sessions, body)
		p.sessionAgents = append(p.sessionAgents, r.Header.Get("User-Agent"))
		if p.sessionStatus != 0 {
			if p.sessionStatus < 0 {
				panic(http.ErrAbortHandler)
			}
			if p.retryAfter != "" {
				w.Header().Set("Retry-After", p.retryAfter)
			}
			w.WriteHeader(p.sessionStatus)
			fmt.Fprint(w, p.sessionBody)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"session": %q, "seed": %d, "expires_at": "2026-10-20T10:00:00Z"}`, testSession, testSeed)
	case "/v1/attempts":
		key := r.Header.Get("Idempotency-Key")
		p.attempts = append(p.attempts, sentAttempt{key: key, auth: r.Header.Get("Authorization"), agent: r.Header.Get("User-Agent"), body: body})
		status := 0
		if len(p.attemptStatus) > 0 {
			status = p.attemptStatus[min(len(p.attempts)-1, len(p.attemptStatus)-1)]
		}
		switch {
		case status == 0:
			if p.stored == nil {
				p.stored = map[string]bool{}
			}
			if p.stored[key] {
				fmt.Fprint(w, `{"attempt": "a", "verification_status": "declared"}`)
				return
			}
			p.stored[key] = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"attempt": "a", "verification_status": "declared"}`)
		case status < 0:
			// Received, then the connection is cut before the answer.
			panic(http.ErrAbortHandler)
		default:
			if p.retryAfter != "" {
				w.Header().Set("Retry-After", p.retryAfter)
			}
			w.WriteHeader(status)
			fmt.Fprint(w, p.attemptBody)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *platform) sent() []sentAttempt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sentAttempt(nil), p.attempts...)
}

func (p *platform) sessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

type checkRig struct {
	*rig
	plat   *platform
	api    string
	labDir string
	ws     string
	queue  *queue.Queue
}

// newCheckRig builds a lab "hello" whose only check passes when the file ok
// exists in the workspace (code HELLO_MISSING otherwise), logs in, and moves
// into the workspace.
func newCheckRig(t *testing.T, meta string) *checkRig {
	t.Helper()
	r := newRig(t, "200")
	c := &checkRig{rig: r, plat: &platform{}}
	mux := http.NewServeMux()
	mux.Handle("/v1/sessions", c.plat)
	mux.Handle("/v1/attempts", c.plat)
	mux.Handle("/", r.dev.handler(&c.api))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c.api = srv.URL
	t.Setenv("SAGNAC_API", srv.URL)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := r.store.Save(&account.Tokens{API: srv.URL, Access: testAccess, Refresh: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour), Scope: "sessions:write attempts:write", Login: testLogin}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	if meta == "" {
		meta = "id: hello\nversion: 1\n"
	}
	meta += `checks:
  - id: verify_ok
    step: 1
    script: checks/verify_ok.sh
    codes: [HELLO_MISSING]
`
	write("_cadre/lib/controle.sh", checkLib, 0o644)
	write("hello/meta.yaml", meta, 0o644)
	write("hello/checks/verify_ok.sh", "#!/bin/sh\n. \"$SAGNAC_LIB\"\nenv > \"$SAGNAC_WORKSPACE/../env.txt\"\n"+
		"[ -f ok ] || sagnac_echec HELLO_MISSING\nsagnac_reussite\n", 0o755)
	write("hello/checks/messages/fr/HELLO_MISSING.txt", "Créez le fichier ok.\n", 0o644)
	c.labDir = filepath.Join(root, "hello")
	c.ws = filepath.Join(c.labDir, "workspace")
	if err := os.Mkdir(c.ws, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(c.ws)
	q, err := queue.Open()
	if err != nil {
		t.Fatal(err)
	}
	c.queue = q
	return c
}

func (c *checkRig) pass(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.ws, "ok"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (c *checkRig) pending(t *testing.T) []queue.Entry {
	t.Helper()
	got, err := c.queue.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (c *checkRig) loggedOut(t *testing.T) {
	t.Helper()
	if err := c.store.Delete(); err != nil {
		t.Fatal(err)
	}
}

// A connected run asks for a session, runs the checks with its seed and
// sends the result under a UUID key with the session in it.
func TestCheckSendsAttemptWithServerSession(t *testing.T) {
	c := newCheckRig(t, "")
	code, stdout, stderr := c.run(t, "check")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, want 1; %s", code, stderr)
	}
	digest, _ := oracle.ChecksDigest(c.labDir)
	if c.plat.sessionCount() != 1 || c.plat.sessions[0]["lab"] != "hello" || c.plat.sessions[0]["version"] != 1.0 ||
		c.plat.sessions[0]["checks_sha256"] != digest {
		t.Fatalf("sessions %v", c.plat.sessions)
	}
	sent := c.plat.sent()
	if len(sent) != 1 {
		t.Fatalf("attempts %v", sent)
	}
	a := sent[0]
	if !reUUID.MatchString(a.key) || a.auth != "Bearer "+testAccess {
		t.Errorf("key %q, auth %q", a.key, a.auth)
	}
	if a.agent != "sagnac/"+version || len(c.plat.sessionAgents) != 1 || c.plat.sessionAgents[0] != "sagnac/"+version {
		t.Errorf("User-Agent of the attempt %q, of the session %v, want sagnac/%s", a.agent, c.plat.sessionAgents, version)
	}
	if a.body["session"] != testSession || a.body["seed"] != float64(testSeed) || a.body["status"] != "fail" ||
		a.body["schema"] != "sagnac-result/2" || a.body["cli"] != "sagnac/1.2.3" {
		t.Errorf("result sent %v", a.body)
	}
	for _, want := range []string{"Lab hello (version 1) : 1 contrôles", "1. verify_ok : à corriger [HELLO_MISSING]",
		"Créez le fichier ok.", i18n.T("check.verdict_1"), i18n.T("check.sent")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if len(c.pending(t)) != 0 {
		t.Error("queue not empty")
	}
	c.pass(t)
	if code, _, stderr := c.run(t, "check"); code != oracle.ExitPass {
		t.Fatalf("passing run: code %d, %s", code, stderr)
	}
}

// The upload never changes the exit code: a 5xx or a cut connection puts the
// attempt in the queue, a 4xx only warns.
func TestOracleCodeUnchangedWhenSendFails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		queued bool
		warn   string
	}{
		{"server error", 500, `{"code": "INTERNAL_ERROR"}`, true, "check.queued"},
		{"cut", -1, "", true, "check.queued"},
		{"schema", 400, `{"code": "RESULT_SCHEMA_VIOLATION", "errors": ["x"]}`, false, "check.not_recorded"},
		{"too large", 413, `{"code": "RESULT_TOO_LARGE"}`, false, "check.not_recorded"},
		{"invalid session", 409, `{"code": "INVALID_SESSION"}`, false, "check.invalid_session"},
		{"unknown kit", 409, `{"code": "UNKNOWN_KIT"}`, false, "check.unknown_kit_sent"},
		{"conflict", 409, `{"code": "IDEMPOTENCY_KEY_CONFLICT"}`, false, "check.not_recorded"},
	}
	for _, tc := range cases {
		for _, passing := range []bool{false, true} {
			c := newCheckRig(t, "")
			if passing {
				c.pass(t)
			}
			c.plat.attemptStatus, c.plat.attemptBody = []int{tc.status}, tc.body
			code, _, stderr := c.run(t, "check")
			want := oracle.ExitFail
			if passing {
				want = oracle.ExitPass
			}
			if code != want {
				t.Errorf("%s: code %d, want %d", tc.name, code, want)
			}
			if !strings.Contains(stderr, i18n.T(tc.warn)) {
				t.Errorf("%s: stderr lacks %s: %q", tc.name, tc.warn, stderr)
			}
			if got := len(c.pending(t)); (got == 1) != tc.queued || got > 1 {
				t.Errorf("%s: %d entries in the queue", tc.name, got)
			}
		}
	}
}

// The first send is cut after the server got it; the next
// run sends the queued attempt again with the same key, the server keeps one
// attempt, and the queue is empty.
func TestQueueResentSameKey(t *testing.T) {
	c := newCheckRig(t, "")
	c.plat.attemptStatus = []int{-1}
	if code, _, _ := c.run(t, "check"); code != oracle.ExitFail {
		t.Fatalf("code %d", code)
	}
	entries := c.pending(t)
	if len(entries) != 1 {
		t.Fatalf("queue %v", entries)
	}
	// The transport of net/http may send a request that carries an
	// Idempotency-Key again after a cut connection: always with the same key.
	firstRun := c.plat.sent()
	first := firstRun[0].key
	for _, a := range firstRun {
		if a.key != first {
			t.Fatalf("first run sent two keys: %v", firstRun)
		}
	}
	n := len(firstRun)
	raw, _ := os.ReadFile(filepath.Join(c.queue.Dir, first+".json"))
	for _, secret := range []string{testAccess, testRefresh, "Bearer"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("queue entry holds %q", secret)
		}
	}
	c.plat.attemptStatus = []int{0}
	code, stdout, stderr := c.run(t, "check")
	if code != oracle.ExitFail {
		t.Fatalf("second run: code %d, %s", code, stderr)
	}
	sent := c.plat.sent()
	if len(sent) != n+2 || sent[n].key != first || sent[n+1].key == first {
		t.Fatalf("keys sent %v", sent)
	}
	if sent[n].body["session"] != sent[0].body["session"] {
		t.Error("queued result changed")
	}
	if len(c.plat.stored) != 2 {
		t.Errorf("%d attempts stored, want 2 (the queued one once, the new one)", len(c.plat.stored))
	}
	if len(c.pending(t)) != 0 {
		t.Error("queue not emptied")
	}
	if !strings.Contains(stdout, i18n.T("check.queue_sent", 1)) {
		t.Errorf("stdout %q", stdout)
	}
}

// An entry older than 30 days is deleted, never sent.
func TestQueueExpiredNotSent(t *testing.T) {
	c := newCheckRig(t, "")
	old := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	c.queue.Now = func() time.Time { return time.Now().Add(-31 * 24 * time.Hour) }
	if err := c.queue.Add(queue.Entry{Key: old, Login: testLogin, API: c.api, Lab: "hello", Version: 1, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	c.queue.Now = time.Now
	c.run(t, "check")
	for _, a := range c.plat.sent() {
		if a.key == old {
			t.Error("expired entry sent")
		}
	}
	if _, err := os.Stat(filepath.Join(c.queue.Dir, old+".json")); !os.IsNotExist(err) {
		t.Error("expired entry kept")
	}
}

// An entry of another server stays in the queue, unsent.
func TestQueueOtherServerKept(t *testing.T) {
	c := newCheckRig(t, "")
	other := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	if err := c.queue.Add(queue.Entry{Key: other, Login: testLogin, API: "https://ailleurs.example", Lab: "hello", Version: 1,
		Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	c.run(t, "check")
	if len(c.plat.sent()) != 1 || len(c.pending(t)) != 1 {
		t.Errorf("sent %d, pending %d", len(c.plat.sent()), len(c.pending(t)))
	}
}

// Not logged in: the check runs here with a local seed, nothing is asked of
// the server, no login is forced, and the person is told why nothing is sent.
func TestOfflineLocalSession(t *testing.T) {
	c := newCheckRig(t, "")
	c.loggedOut(t)
	code, stdout, stderr := c.run(t, "check", "--json")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, %s", code, stderr)
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not the JSON result: %v\n%s", err, stdout)
	}
	if r["session"] != "local" {
		t.Errorf("session %v", r["session"])
	}
	if c.plat.sessionCount() != 0 || len(c.plat.sent()) != 0 || len(c.dev.codeForms) != 0 {
		t.Error("the server was called")
	}
	if !strings.Contains(stderr, i18n.T("check.not_logged_in")) {
		t.Errorf("stderr %q", stderr)
	}
	if len(c.pending(t)) != 0 {
		t.Error("queued while not logged in")
	}
}

// Logged in but the server is unreachable: local seed, result queued.
func TestUnreachableServerQueuesLocalResult(t *testing.T) {
	c := newCheckRig(t, "")
	c.plat.sessionStatus = -1
	code, _, stderr := c.run(t, "check")
	if code != oracle.ExitFail {
		t.Fatalf("code %d", code)
	}
	entries := c.pending(t)
	if len(entries) != 1 || len(c.plat.sent()) != 0 {
		t.Fatalf("queue %v, sent %v", entries, c.plat.sent())
	}
	var r map[string]any
	_ = json.Unmarshal(entries[0].Result, &r)
	if r["session"] != "local" || entries[0].Lab != "hello" || entries[0].Version != 1 || entries[0].API != c.api {
		t.Errorf("entry %+v, result %v", entries[0], r)
	}
	if !strings.Contains(stderr, i18n.T("check.queued")) {
		t.Errorf("stderr %q", stderr)
	}
}

// 403 ENTITLEMENT_INACTIVE on the session: local check, oracle code,
// message, nothing sent or queued. On the attempt: message, nothing queued.
func TestEntitlementInactiveNotQueued(t *testing.T) {
	entitlement := `{"code": "ENTITLEMENT_INACTIVE", "state": "expired"}`
	c := newCheckRig(t, "")
	c.plat.sessionStatus, c.plat.sessionBody = 403, entitlement
	code, stdout, stderr := c.run(t, "check", "--json")
	if code != oracle.ExitFail {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(stdout, `"session": "local"`) {
		t.Errorf("stdout %s", stdout)
	}
	if !strings.Contains(stderr, i18n.T("check.entitlement_expired")) || len(c.plat.sent()) != 0 || len(c.pending(t)) != 0 {
		t.Errorf("stderr %q, sent %d, pending %d", stderr, len(c.plat.sent()), len(c.pending(t)))
	}

	c = newCheckRig(t, "")
	c.plat.attemptStatus, c.plat.attemptBody = []int{403}, entitlement
	code, _, stderr = c.run(t, "check")
	if code != oracle.ExitFail || !strings.Contains(stderr, i18n.T("check.entitlement_expired")) || len(c.pending(t)) != 0 {
		t.Errorf("attempt refused: code %d, stderr %q, pending %d", code, stderr, len(c.pending(t)))
	}
}

// 409 UNKNOWN_KIT on the session: the kit was changed; local check, sagnac
// pull proposed, nothing sent.
func TestUnknownKitOnSession(t *testing.T) {
	c := newCheckRig(t, "")
	c.plat.sessionStatus, c.plat.sessionBody = 409, `{"code": "UNKNOWN_KIT"}`
	code, _, stderr := c.run(t, "check")
	if code != oracle.ExitFail || !strings.Contains(stderr, i18n.T("check.unknown_kit", "hello")) ||
		len(c.plat.sent()) != 0 || len(c.pending(t)) != 0 {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

// 426 CLI_TOO_OLD: the check is done, the update is asked, the code is the
// oracle's, nothing is queued.
func TestCLITooOld(t *testing.T) {
	c := newCheckRig(t, "")
	c.pass(t)
	c.plat.attemptStatus, c.plat.attemptBody = []int{426}, `{"code": "CLI_TOO_OLD", "minimum": "0.2.0"}`
	code, stdout, stderr := c.run(t, "check")
	if code != oracle.ExitPass {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(stdout, i18n.T("check.verdict_0")) || !strings.Contains(stderr, i18n.T("check.cli_too_old", "0.2.0")) {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}
	if len(c.pending(t)) != 0 {
		t.Error("queued")
	}
}

// The network is cut during the checks unless meta.yaml says
// network_during_check: true.
func TestNetworkCutByDefault(t *testing.T) {
	for meta, want := range map[string]bool{"": true, "id: hello\nversion: 1\nnetwork_during_check: true\n": false,
		"id: hello\nversion: 1\nnetwork_during_check: false\n": true} {
		c := newCheckRig(t, meta)
		var asked []bool
		old := verify
		verify = func(ctx context.Context, lab *oracle.Lab, ws string, seed int64, noNetwork bool) (*oracle.Result, error) {
			asked = append(asked, noNetwork)
			return old(ctx, lab, ws, seed, false)
		}
		c.run(t, "check")
		verify = old
		if len(asked) != 1 || asked[0] != want {
			t.Errorf("%q: noNetwork %v, want %v", meta, asked, want)
		}
	}
}

// Without a way to cut the network, the person is warned and the checks run
// with the network (formative check, docs/contrat.md, section 3).
func TestNetworkCutUnavailableWarns(t *testing.T) {
	c := newCheckRig(t, "")
	var asked []bool
	old := verify
	t.Cleanup(func() { verify = old })
	verify = func(ctx context.Context, lab *oracle.Lab, ws string, seed int64, noNetwork bool) (*oracle.Result, error) {
		asked = append(asked, noNetwork)
		if noNetwork {
			return nil, oracle.ErrNetworkCutUnavailable
		}
		return old(ctx, lab, ws, seed, false)
	}
	code, _, stderr := c.run(t, "check")
	if code != oracle.ExitFail || len(asked) != 2 || !asked[0] || asked[1] {
		t.Fatalf("code %d, calls %v", code, asked)
	}
	if !strings.Contains(stderr, i18n.T("check.network_cut_unavailable")) {
		t.Errorf("stderr %q", stderr)
	}
}

// The environment a check receives holds no token, no variable of the
// account, no session bus, and a configuration directory that is not the
// person's (secops C3).
func TestCheckEnvironmentHasNoToken(t *testing.T) {
	c := newCheckRig(t, "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/tmp/bus")
	t.Setenv("SAGNAC_TOKEN", "x")
	userConfig := os.Getenv("XDG_CONFIG_HOME")
	if code, _, stderr := c.run(t, "check"); code != oracle.ExitFail {
		t.Fatalf("code %d, %s", code, stderr)
	}
	raw, err := os.ReadFile(filepath.Join(c.labDir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(raw)
	for _, banned := range []string{testAccess, testRefresh, "SAGNAC_API", "SAGNAC_TOKEN", "DBUS_SESSION_BUS_ADDRESS",
		"XDG_STATE_HOME", "XDG_CONFIG_HOME=" + userConfig + "\n", "Bearer"} {
		if strings.Contains(env, banned) {
			t.Errorf("the check received %q:\n%s", banned, env)
		}
	}
	if !strings.Contains(env, "XDG_CONFIG_HOME=") || !strings.Contains(env, "SAGNAC_SEED=777") {
		t.Errorf("environment:\n%s", env)
	}
}

// With --session: device flow in memory, family revoked at the end, nothing
// written to the queue (no directory created), stored login untouched.
func TestCheckSessionWritesNothing(t *testing.T) {
	c := newCheckRig(t, "")
	c.plat.attemptStatus = []int{500}
	code, _, stderr := c.run(t, "check", "--session")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, %s", code, stderr)
	}
	if len(c.dev.codeForms) != 1 || len(c.dev.revokeForms) != 1 {
		t.Errorf("device flows %d, revocations %d", len(c.dev.codeForms), len(c.dev.revokeForms))
	}
	if _, err := os.Stat(c.queue.Dir); !os.IsNotExist(err) {
		t.Error("queue directory created with --session")
	}
	if !strings.Contains(stderr, i18n.T("check.not_sent_session")) {
		t.Errorf("stderr %q", stderr)
	}
	if got, _ := c.store.Load(); got == nil || got.Access != testAccess {
		t.Errorf("stored login touched: %+v", got)
	}
	if len(c.plat.sent()) != 1 || c.plat.sent()[0].body["session"] != testSession {
		t.Errorf("sent %v", c.plat.sent())
	}
}

// With --json, stdout is the result alone, the one that was sent.
func TestCheckJSONIsTheSentResult(t *testing.T) {
	c := newCheckRig(t, "")
	_, stdout, _ := c.run(t, "check", "--json")
	var shown map[string]any
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	sent := c.plat.sent()
	if len(sent) != 1 || fmt.Sprint(sent[0].body) != fmt.Sprint(shown) {
		t.Errorf("shown %v\nsent %v", shown, sent)
	}
}

func TestPurge(t *testing.T) {
	c := newCheckRig(t, "")
	for _, k := range []string{"0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d", "1c4a2d8f-5b6e-4a3c-8d2f-3e4a5b6c7d8e"} {
		if err := c.queue.Add(queue.Entry{Key: k, Login: testLogin, API: c.api, Lab: "hello", Version: 1, Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, _ := c.run(t, "purge")
	if code != exitOK || !strings.Contains(stdout, i18n.T("purge.done", 2)) || len(c.pending(t)) != 0 {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	code, stdout, _ = c.run(t, "purge")
	if code != exitOK || !strings.Contains(stdout, i18n.T("purge.empty")) {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	if code, _, _ := c.run(t, "purge", "x"); code != exitUsage {
		t.Errorf("purge x: code %d", code)
	}
}

func TestCheckArguments(t *testing.T) {
	c := newCheckRig(t, "")
	cases := []struct {
		dir  string
		args []string
		code int
		want string
	}{
		{"", []string{"check", "--bad"}, exitUsage, i18n.T("error.unknown_option", "--bad")},
		{"", []string{"check", "hello", "autre"}, exitUsage, i18n.T("error.unexpected_argument", "autre", "check")},
		{"", []string{"check", "Bad_Lab"}, exitUsage, i18n.T("check.invalid_lab", "Bad_Lab")},
		{"", []string{"check", "autre"}, exitUsage, i18n.T("check.other_lab", "hello", "autre")},
		{"/", []string{"check"}, exitUsage, i18n.T("check.no_lab", "/")},
	}
	for _, tc := range cases {
		if tc.dir != "" {
			t.Chdir(tc.dir)
		}
		code, _, stderr := c.run(t, tc.args...)
		if code != tc.code || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: code %d, stderr %q", tc.args, code, stderr)
		}
	}
	// From the parent of the lab, the name finds it.
	t.Chdir(filepath.Dir(c.labDir))
	if code, _, stderr := c.run(t, "check", "hello"); code != oracle.ExitFail {
		t.Errorf("from the parent: code %d, %s", code, stderr)
	}
	// No workspace yet.
	if err := os.RemoveAll(c.ws); err != nil {
		t.Fatal(err)
	}
	t.Chdir(c.labDir)
	code, _, stderr := c.run(t, "check")
	real, _ := filepath.EvalSymlinks(c.labDir)
	if code != exitUsage || !strings.Contains(stderr, i18n.T("check.no_workspace", real)) {
		t.Errorf("no workspace: code %d, %q", code, stderr)
	}
}

// Machine output does not depend on the language: check --json on the
// frozen kit hello (testdata/hello-v5), LANG=fr_FR.UTF-8 then --lang xx,
// gives the same JSON but for started_at and duration_ms.
func TestTwoLocalesSameJSON(t *testing.T) {
	setBuildInfo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SAGNAC_API", "http://127.0.0.1:1")
	oldOpen := openStore
	openStore = func(string, bool, io.Writer) (account.TokenStore, error) { return &account.MemoryStore{}, nil }
	oldSeed := localSeed
	localSeed = func() (int64, error) { return 4242, nil }
	t.Cleanup(func() { openStore, localSeed = oldOpen, oldSeed })
	k, _ := testkit.Load(t, "hello-v5")
	root := testkit.Install(t, "hello-v5")
	ws := filepath.Join(root, k.Lab, "workspace")
	if out, err := exec.Command("cp", "-R", filepath.Join(root, k.Lab, "starter"), ws).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Chdir(ws)
	var outputs []map[string]any
	for _, call := range []struct {
		args []string
		env  map[string]string
	}{
		{[]string{"check", "--json"}, map[string]string{"LANG": "fr_FR.UTF-8"}},
		{[]string{"--lang", "xx", "check", "hello", "--json"}, nil},
	} {
		var out, errOut bytes.Buffer
		code := run(call.args, func(k string) string { return call.env[k] }, &out, &errOut)
		if code != oracle.ExitFail {
			t.Fatalf("%v: code %d, %s", call.args, code, errOut.String())
		}
		var r map[string]any
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%v: %v\n%s", call.args, err, out.String())
		}
		delete(r, "started_at")
		delete(r, "duration_ms")
		for _, c := range r["checks"].([]any) {
			delete(c.(map[string]any), "duration_ms")
		}
		outputs = append(outputs, r)
	}
	a, _ := json.Marshal(outputs[0])
	b, _ := json.Marshal(outputs[1])
	if !bytes.Equal(a, b) {
		t.Errorf("JSON differs:\n%s\n%s", a, b)
	}
}

// The message of a queued result says when it will be sent, exactly.
func TestQueuedMessageExact(t *testing.T) {
	want := "Le serveur n'a pas pu recevoir le résultat. Il est gardé sur ce poste et sera envoyé au prochain " +
		"« sagnac check », s'il a lieu dans les 30 jours."
	if got := i18n.T("check.queued"); got != want {
		t.Errorf("check.queued = %q", got)
	}
	c := newCheckRig(t, "")
	c.plat.attemptStatus = []int{503}
	_, _, stderr := c.run(t, "check")
	if !strings.Contains(stderr, want+"\n") {
		t.Errorf("stderr %q", stderr)
	}
}

// Deferred resend: an attempt queued
// on day 0 is sent with the same key and the same result on day 3; on
// day 31 it is deleted unsent. The clock of the queue is simulated.
func TestQueueDeferredResend(t *testing.T) {
	day0 := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		after time.Duration
		sent  bool
	}{{3 * 24 * time.Hour, true}, {31 * 24 * time.Hour, false}} {
		c := newCheckRig(t, "")
		oldNow := now
		now = func() time.Time { return day0 }
		c.plat.attemptStatus = []int{-1}
		c.run(t, "check")
		queued := c.pending(t)
		if len(queued) != 1 {
			now = oldNow
			t.Fatalf("queue %v", queued)
		}
		n := len(c.plat.sent())
		now = func() time.Time { return day0.Add(tc.after) }
		c.plat.attemptStatus = []int{0}
		c.run(t, "check")
		now = oldNow
		sent := c.plat.sent()[n:]
		resent := len(sent) > 0 && sent[0].key == queued[0].Key
		if resent != tc.sent {
			t.Errorf("after %s: resent %v, sent %v", tc.after, resent, sent)
		}
		if resent && fmt.Sprint(sent[0].body) != fmt.Sprint(c.plat.sent()[0].body) {
			t.Error("queued result changed")
		}
		if _, err := os.Stat(filepath.Join(c.queue.Dir, queued[0].Key+".json")); !os.IsNotExist(err) {
			t.Errorf("after %s: entry kept", tc.after)
		}
	}
}

// A refusal of the server while the queue is sent (here 426 CLI_TOO_OLD)
// removes the entry: a 4xx is never kept.
func TestQueueEntryDroppedOn426DuringFlush(t *testing.T) {
	c := newCheckRig(t, "")
	old := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	if err := c.queue.Add(queue.Entry{Key: old, Login: testLogin, API: c.api, Lab: "hello", Version: 1, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	c.plat.attemptStatus, c.plat.attemptBody = []int{426, 0}, `{"code": "CLI_TOO_OLD", "minimum": "0.2.0"}`
	_, _, stderr := c.run(t, "check")
	if sent := c.plat.sent(); len(sent) != 2 || sent[0].key != old {
		t.Fatalf("sent %v", sent)
	}
	if len(c.pending(t)) != 0 || !strings.Contains(stderr, i18n.T("check.queue_dropped", 1)) {
		t.Errorf("pending %d, stderr %q", len(c.pending(t)), stderr)
	}
}

// A development binary whose version the server cannot read gets its own
// message on 426, not "too old".
func TestDevBuildMessageOn426(t *testing.T) {
	c := newCheckRig(t, "")
	c.plat.attemptStatus, c.plat.attemptBody = []int{426}, `{"code": "CLI_TOO_OLD", "minimum": "0.1.0"}`
	setBuildInfo(t)
	old := version
	version = "27f6614-dirty"
	t.Cleanup(func() { version = old })
	var out, errOut bytes.Buffer
	code := run([]string{"check"}, noEnv, &out, &errOut)
	if code != oracle.ExitFail || !strings.Contains(errOut.String(), i18n.T("check.dev_build")) ||
		strings.Contains(errOut.String(), i18n.T("check.cli_too_old", "0.1.0")) {
		t.Errorf("code %d, stderr %q", code, errOut.String())
	}
}

// Ctrl-C during the checks: the oracle gives ErrInterrupted; the command
// says so and returns 130, sends nothing and queues nothing.
func TestCheckInterrupted(t *testing.T) {
	c := newCheckRig(t, "")
	old := verify
	t.Cleanup(func() { verify = old })
	verify = func(ctx context.Context, lab *oracle.Lab, ws string, seed int64, noNetwork bool) (*oracle.Result, error) {
		return nil, oracle.ErrInterrupted
	}
	code, stdout, stderr := c.run(t, "check")
	if code != 130 {
		t.Errorf("code %d, want 130", code)
	}
	if !strings.Contains(stderr, i18n.T("check.interrupted")) || strings.Contains(stdout, i18n.T("check.verdict_1")) {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}
	if len(c.plat.sent()) != 0 || len(c.pending(t)) != 0 {
		t.Error("interrupted result sent or queued")
	}
}

// End to end: SIGINT to the process group of a real check (sleep 30) makes
// the command return 130 within 3 s, with no process left.
func TestCheckInterruptedBySignal(t *testing.T) {
	c := newCheckRig(t, "")
	c.loggedOut(t)
	script := filepath.Join(c.labDir, "checks", "verify_ok.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n. \"$SAGNAC_LIB\"\nsleep 30 &\necho $! > \"$SAGNAC_WORKSPACE/child.pid\"\nsleep 30\nsagnac_reussite\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pidFile := filepath.Join(c.ws, "child.pid")
	go func() {
		for i := 0; i < 300; i++ {
			if raw, err := os.ReadFile(pidFile); err == nil && len(raw) > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	start := time.Now()
	code := runCheck(ctx, nil, &out, &errOut)
	if elapsed := time.Since(start); elapsed > 3*time.Second+time.Second {
		t.Errorf("returned after %s", elapsed)
	}
	if code != 130 {
		t.Errorf("code %d, stderr %q", code, errOut.String())
	}
	raw, _ := os.ReadFile(pidFile)
	var pid int
	fmt.Sscan(string(raw), &pid)
	time.Sleep(100 * time.Millisecond)
	if pid > 0 && syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("process %d left", pid)
	}
}

// logout empties the queue; login does too when it replaces a connection.
func TestLoginLogoutPurgeQueue(t *testing.T) {
	add := func(t *testing.T, c *checkRig) {
		t.Helper()
		if err := c.queue.Add(queue.Entry{Key: "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d", Login: testLogin, API: c.api, Lab: "hello", Version: 1,
			Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	c := newCheckRig(t, "")
	add(t, c)
	if code, stdout, _ := c.run(t, "logout"); code != exitOK || len(c.pending(t)) != 0 ||
		!strings.Contains(stdout, i18n.T("account.queue_purged", 1)) {
		t.Errorf("logout: code %d, pending %d, stdout %q", code, len(c.pending(t)), stdout)
	}

	c = newCheckRig(t, "")
	add(t, c)
	if code, _, stderr := c.run(t, "login"); code != exitOK || len(c.pending(t)) != 0 {
		t.Errorf("login over a connection: code %d, pending %d, %q", code, len(c.pending(t)), stderr)
	}

	// No tokens to replace (expired, revoked, never stored): the queue may
	// still hold the attempts of another account, so it goes too.
	c = newCheckRig(t, "")
	c.loggedOut(t)
	add(t, c)
	if code, _, stderr := c.run(t, "login"); code != exitOK || len(c.pending(t)) != 0 {
		t.Errorf("login without tokens: code %d, pending %d, %q", code, len(c.pending(t)), stderr)
	}
}

// offlineCopy writes an offline copy of the instructions of hello and returns
// a function that says whether it is still there.
func offlineCopy(t *testing.T) func() bool {
	t.Helper()
	if _, _, err := instructions.Store("hello", 1, &instructions.Page{HTML: []byte("<p>A</p>"), Text: []byte("A")}); err != nil {
		t.Fatal(err)
	}
	return func() bool {
		_, _, _, ok := instructions.Local("hello", 0)
		return ok
	}
}

// Review I1: the connection of A ends by itself (invalid_grant: 30 days
// without use, family expired, device revoked on the site), B logs in on the
// same machine, then checks: nothing of A reaches the account of B. The
// offline instructions of A are gone too (M8).
func TestEndedConnectionNeverSendsToNextAccount(t *testing.T) {
	c := newCheckRig(t, "")
	tokA, _ := c.store.Load()
	tokA.ExpiresAt = time.Now().Add(-time.Minute)
	if err := c.store.Save(tokA); err != nil {
		t.Fatal(err)
	}
	keyA := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	if err := c.queue.Add(queue.Entry{Key: keyA, Login: testLogin, API: c.api, Lab: "hello", Version: 1,
		Result: json.RawMessage(`{"session": "local"}`)}); err != nil {
		t.Fatal(err)
	}
	copyLeft := offlineCopy(t)
	// The refresh of A is refused, then the device flow of B succeeds.
	c.dev.answers = []string{`{"error": "invalid_grant"}`, "200"}
	if code, _, stderr := c.run(t, "check"); code != oracle.ExitFail || !strings.Contains(stderr, i18n.T("check.not_logged_in")) {
		t.Fatalf("check after invalid_grant: code %d, %q", code, stderr)
	}
	if left, _ := c.store.Load(); left != nil {
		t.Fatalf("tokens of A kept: %+v", left)
	}
	if len(c.pending(t)) != 0 || copyLeft() {
		t.Errorf("after invalid_grant: pending %d, offline copy kept %v", len(c.pending(t)), copyLeft())
	}
	if code, _, stderr := c.run(t, "login"); code != exitOK {
		t.Fatalf("login of B: code %d, %q", code, stderr)
	}
	tokB, _ := c.store.Load()
	if tokB == nil || !reUUID.MatchString(tokB.Login) || tokB.Login == testLogin {
		t.Fatalf("connection of B: %+v", tokB)
	}
	c.pass(t)
	if code, _, stderr := c.run(t, "check"); code != oracle.ExitPass {
		t.Fatalf("check of B: code %d, %q", code, stderr)
	}
	sent := c.plat.sent()
	if len(sent) != 1 || sent[0].key == keyA || sent[0].body["session"] != testSession {
		t.Errorf("sent to B: %+v", sent)
	}
}

// Second lock: an entry of another connection of the same server that is
// still in the queue (written by a process that missed the purge) is deleted
// unsent; the entries of this connection are sent.
func TestFlushDropsEntriesOfAnotherLogin(t *testing.T) {
	c := newCheckRig(t, "")
	other := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	mine := "1c4a2d8f-5b6e-4a3c-8d2f-3e4a5b6c7d8e"
	for key, login := range map[string]string{other: "4f7d5a1c-8e9b-4d6f-9a5c-6b7d8e9f0a1b", mine: testLogin} {
		if err := c.queue.Add(queue.Entry{Key: key, Login: login, API: c.api, Lab: "hello", Version: 1,
			Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, stderr := c.run(t, "check"); code != oracle.ExitFail {
		t.Fatalf("code %d, %q", code, stderr)
	}
	var keys []string
	for _, a := range c.plat.sent() {
		keys = append(keys, a.key)
	}
	if len(keys) != 2 || keys[0] != mine || keys[1] == other {
		t.Errorf("keys sent %v, want %s then the new one", keys, mine)
	}
	if len(c.pending(t)) != 0 {
		t.Errorf("queue %v", c.pending(t))
	}
}

// The queued attempt of a run carries the connection of the stored tokens;
// tokens of an older version get one first.
func TestQueuedEntryCarriesTheLogin(t *testing.T) {
	c := newCheckRig(t, "")
	tok, _ := c.store.Load()
	tok.Login = ""
	_ = c.store.Save(tok)
	c.plat.attemptStatus = []int{503}
	c.run(t, "check")
	entries := c.pending(t)
	tok, _ = c.store.Load()
	if len(entries) != 1 || !reUUID.MatchString(tok.Login) || entries[0].Login != tok.Login {
		t.Errorf("entries %+v, tokens %+v", entries, tok)
	}
}

// A command that logs in by itself (pull, open) purges like login, and a
// --session run touches neither the queue nor the offline copies.
func TestImplicitLoginPurges(t *testing.T) {
	c := newCheckRig(t, "")
	c.loggedOut(t)
	add := func() {
		if err := c.queue.Add(queue.Entry{Key: "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d", Login: testLogin, API: c.api,
			Lab: "hello", Version: 1, Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	add()
	copyLeft := offlineCopy(t)
	if _, cleanup, err := ensureTokens(context.Background(), true, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	} else {
		cleanup()
	}
	if len(c.pending(t)) != 1 || !copyLeft() {
		t.Fatalf("--session purged: pending %d, copy %v", len(c.pending(t)), copyLeft())
	}
	if _, _, err := ensureTokens(context.Background(), false, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(c.pending(t)) != 0 || copyLeft() {
		t.Errorf("implicit login: pending %d, copy %v", len(c.pending(t)), copyLeft())
	}
	if tok, _ := c.store.Load(); tok == nil || !reUUID.MatchString(tok.Login) {
		t.Errorf("tokens %+v", tok)
	}
}

// Ctrl-C while the result is being sent: the person is told that it was not
// sent; nothing is queued (assumed), and an entry of the queue being sent
// again is kept, not dropped.
func TestCheckInterruptedDuringSend(t *testing.T) {
	c := newCheckRig(t, "")
	old := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	if err := c.queue.Add(queue.Entry{Key: old, Login: testLogin, API: c.api, Lab: "hello", Version: 1, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	c.plat.onAttempt = func() {
		cancel()
		<-release
	}
	defer close(release)
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	code := runCheck(ctx, nil, &out, &errOut)
	if code != oracle.ExitFail {
		t.Errorf("code %d, want the oracle's", code)
	}
	if !strings.Contains(errOut.String(), i18n.T("check.send_interrupted")) {
		t.Errorf("stderr %q", errOut.String())
	}
	pending := c.pending(t)
	if len(pending) != 1 || pending[0].Key != old {
		t.Errorf("queue %v", pending)
	}
}

// 429 TOO_MANY_REQUESTS on the attempt: queued like a 5xx, with a message
// that gives the delay of Retry-After; the exit code stays the oracle's.
func TestRateLimitedAttemptQueued(t *testing.T) {
	for _, tc := range []struct {
		name, body, retryAfter, want string
	}{
		{"code and delay", `{"code": "TOO_MANY_REQUESTS"}`, "90", i18n.T("check.rate_limited_attempt", "90 s")},
		{"minutes", `{"code": "TOO_MANY_REQUESTS"}`, "600", i18n.T("check.rate_limited_attempt", "10 min")},
		{"no header, no code", ``, "", i18n.T("check.rate_limited_attempt_later")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCheckRig(t, "")
			c.plat.attemptStatus, c.plat.attemptBody, c.plat.retryAfter = []int{429}, tc.body, tc.retryAfter
			code, _, stderr := c.run(t, "check")
			if code != oracle.ExitFail {
				t.Fatalf("code %d, %s", code, stderr)
			}
			if len(c.pending(t)) != 1 {
				t.Errorf("pending %d", len(c.pending(t)))
			}
			if !strings.Contains(stderr, tc.want+"\n") || !strings.Contains(stderr, i18n.T("check.queued")) ||
				strings.Contains(stderr, i18n.T("error.unexpected_response")) {
				t.Errorf("stderr %q", stderr)
			}
		})
	}
}

// 429 on the session: local check with a message that says how long to wait;
// nothing sent, nothing queued, the oracle's exit code.
func TestRateLimitedSessionLocal(t *testing.T) {
	c := newCheckRig(t, "")
	c.plat.sessionStatus, c.plat.sessionBody, c.plat.retryAfter = 429, `{"code": "TOO_MANY_REQUESTS"}`, "30"
	code, stdout, stderr := c.run(t, "check", "--json")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, %s", code, stderr)
	}
	if !strings.Contains(stdout, `"session": "local"`) {
		t.Errorf("stdout %s", stdout)
	}
	if !strings.Contains(stderr, i18n.T("check.rate_limited_session", "30 s")) ||
		strings.Contains(stderr, i18n.T("error.unexpected_response")) {
		t.Errorf("stderr %q", stderr)
	}
	if len(c.plat.sent()) != 0 || len(c.pending(t)) != 0 {
		t.Errorf("sent %d, pending %d", len(c.plat.sent()), len(c.pending(t)))
	}
}

// A queued attempt answered 429 while the queue is sent is kept, never
// dropped like a 4xx refusal.
func TestQueueEntryKeptOn429DuringFlush(t *testing.T) {
	c := newCheckRig(t, "")
	old := "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	if err := c.queue.Add(queue.Entry{Key: old, Login: testLogin, API: c.api, Lab: "hello", Version: 1, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	c.plat.attemptStatus, c.plat.attemptBody, c.plat.retryAfter = []int{429}, `{"code": "TOO_MANY_REQUESTS"}`, "60"
	_, _, stderr := c.run(t, "check")
	keys := map[string]bool{}
	for _, e := range c.pending(t) {
		keys[e.Key] = true
	}
	if !keys[old] || len(keys) != 2 || strings.Contains(stderr, i18n.T("check.queue_dropped", 1)) {
		t.Errorf("pending %v, stderr %q", keys, stderr)
	}
}

// clusterV3Meta is the head of a lab of the cluster contract v3: nothing in
// meta.yaml tells the version, only _cadre/clusters/sagnac-b3.conf.
const clusterV3Meta = `id: hello
version: 1
cluster: sagnac-b3
docker: true
tools: [k3d, kubectl, k3s, k3s-airgap, k3d-tools]
preconditions:
  - code: CLUSTER_DIRTY
    description: "objets restants"
  - code: CLUSTER_BUSY
    description: "cluster occupé"
`

// A kit whose cluster names a contract this CLI does not know (CONTRAT=9) or
// a key it does not know is refused before anything runs: exit 3, the file at
// fault named, no session asked, nothing sent or queued (docs/contrat.md,
// section 7 bis).
func TestCheckRefusesUnknownClusterContract(t *testing.T) {
	for _, conf := range []string{"API_PORT=6560\nCONTRAT=9\n", "API_PORT=6560\nCONTRAT=3\nNODES=2\n"} {
		c := newCheckRig(t, clusterV3Meta)
		path := filepath.Join(filepath.Dir(c.labDir), "_cadre", "clusters", "sagnac-b3.conf")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := c.run(t, "check")
		if code != oracle.ExitOracle || !strings.Contains(stderr, i18n.T("check.invalid_kit", "clusters/sagnac-b3.conf")) {
			t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if c.plat.sessionCount() != 0 || len(c.plat.sent()) != 0 || len(c.pending(t)) != 0 {
			t.Errorf("session or attempt despite a refused kit")
		}
	}
}
