package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

const (
	testUserCode   = "BCDF-GHJK"
	testDeviceCode = "dc_SECRETDEVICECODE"
	testAccess     = "sgn_a_SECRETACCESS"
	testRefresh    = "sgn_r_SECRETREFRESH"
)

// device is the device flow of the platform, scripted: the answers of
// /v1/token are consumed in order, the last one repeats.
type device struct {
	mu          sync.Mutex
	codeStatus  []codeAnswer // answers of /v1/device/code, the last repeats
	codeBody    string       // used when codeStatus is empty
	answers     []string     // /v1/token bodies; "200" is the success
	statuses    []int
	retryAfter  map[int]string // Retry-After by index of answer
	codeForms   []url.Values
	tokenForms  []url.Values
	revokeForms []url.Values
	revokeCode  int
	agents      []string // User-Agent of every request
}

type codeAnswer struct {
	status int
	retry  string
	body   string
}

func (d *device) polls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.tokenForms)
}

func (d *device) codeJSON(extra string) string {
	return fmt.Sprintf(`{"device_code": %q, "user_code": %q, "verification_uri": "%s/appareil/", "expires_in": 600, "interval": 5%s}`,
		testDeviceCode, testUserCode, "%API%", extra)
}

func (d *device) handler(api *string) http.Handler {
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		d.mu.Lock()
		d.agents = append(d.agents, r.Header.Get("User-Agent"))
		d.mu.Unlock()
	}
	mux.HandleFunc("/v1/device/code", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = r.ParseForm()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.codeForms = append(d.codeForms, r.PostForm)
		ans := codeAnswer{status: 200, body: d.codeBody}
		if len(d.codeStatus) > 0 {
			i := min(len(d.codeForms)-1, len(d.codeStatus)-1)
			ans = d.codeStatus[i]
		}
		if ans.body == "" {
			ans.body = d.codeJSON("")
		}
		w.Header().Set("Content-Type", "application/json")
		if ans.retry != "" {
			w.Header().Set("Retry-After", ans.retry)
		}
		w.WriteHeader(ans.status)
		fmt.Fprint(w, strings.ReplaceAll(ans.body, "%API%", *api))
	})
	mux.HandleFunc("/v1/token", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = r.ParseForm()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.tokenForms = append(d.tokenForms, r.PostForm)
		i := min(len(d.tokenForms)-1, len(d.answers)-1)
		w.Header().Set("Content-Type", "application/json")
		if d.answers[i] == "ABORT" {
			panic(http.ErrAbortHandler) // the connection is cut
		}
		if ra := d.retryAfter[i]; ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		if d.answers[i] == "200" {
			fmt.Fprintf(w, `{"access_token": %q, "token_type": "Bearer", "expires_in": 3600, "refresh_token": %q, "scope": "attempts:write kits:read"}`, testAccess, testRefresh)
			return
		}
		status := 400
		if i < len(d.statuses) {
			status = d.statuses[i]
		}
		w.WriteHeader(status)
		fmt.Fprint(w, d.answers[i])
	})
	mux.HandleFunc("/v1/revoke", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = r.ParseForm()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.revokeForms = append(d.revokeForms, r.PostForm)
		if d.revokeCode != 0 {
			w.WriteHeader(d.revokeCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})
	return mux
}

// failingStore refuses to save, like a locked keychain.
type failingStore struct{ account.MemoryStore }

func (f *failingStore) Save(*account.Tokens) error { return account.ErrKeyringFailed }

type rig struct {
	dev          *device
	srv          *httptest.Server
	store        account.TokenStore
	sleeps       []time.Duration
	session      []bool
	sessionStore account.TokenStore
}

// newRig starts the platform, points SAGNAC_API at it, and replaces the
// keyring by an in-memory store and the waiting by a recorder.
func newRig(t *testing.T, answers ...string) *rig {
	t.Helper()
	r := &rig{dev: &device{answers: answers}, store: &account.MemoryStore{}}
	var api string
	r.srv = httptest.NewServer(r.dev.handler(&api))
	api = r.srv.URL
	t.Cleanup(r.srv.Close)
	t.Setenv("SAGNAC_API", r.srv.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A login purges the queue and the offline instructions: never the
	// user's own.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	oldOpen, oldSleep := openStore, sleep
	openStore = func(api string, session bool, notice io.Writer) (account.TokenStore, error) {
		r.session = append(r.session, session)
		if session {
			r.sessionStore = &account.MemoryStore{}
			return r.sessionStore, nil
		}
		return r.store, nil
	}
	sleep = func(ctx context.Context, d time.Duration) error {
		r.sleeps = append(r.sleeps, d)
		return ctx.Err()
	}
	t.Cleanup(func() { openStore, sleep = oldOpen, oldSleep })
	return r
}

func (r *rig) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return execute(t, args...)
}

func seconds(n ...int) []time.Duration {
	var out []time.Duration
	for _, s := range n {
		out = append(out, time.Duration(s)*time.Second)
	}
	return out
}

func TestLoginShowsCodeAndURL(t *testing.T) {
	r := newRig(t, "200")
	code, stdout, stderr := r.run(t, "login")
	if code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, r.srv.URL+"/appareil/") || !strings.Contains(stdout, testUserCode) {
		t.Fatalf("stdout lacks the address or the code:\n%s", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, testUserCode) && strings.Contains(line, "http") {
			t.Errorf("the code and the address are on one line: %q", line)
		}
	}
	if strings.Contains(stdout, "?") && strings.Contains(stdout, "user_code") || strings.Contains(stdout, "/appareil/?") ||
		strings.Contains(stdout, "/appareil/"+testUserCode) {
		t.Errorf("an address carries the code:\n%s", stdout)
	}
	all := stdout + stderr
	for _, secret := range []string{testDeviceCode, testAccess, testRefresh} {
		if strings.Contains(all, secret) {
			t.Errorf("secret %q printed", secret)
		}
	}
	tok, _ := r.store.Load()
	if tok == nil || tok.Access != testAccess || tok.Refresh != testRefresh || tok.API != r.srv.URL {
		t.Errorf("stored tokens = %+v", tok)
	}
	if got := r.dev.tokenForms[0]; got.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" ||
		got.Get("device_code") != testDeviceCode || got.Get("client_id") != "sagnac-cli" {
		t.Errorf("token form = %v", got)
	}
	if fmt.Sprint(r.session) != "[false]" {
		t.Errorf("store opened with session %v", r.session)
	}
}

func TestLoginSendsBoundedDeviceName(t *testing.T) {
	r := newRig(t, "200")
	if code, _, stderr := r.run(t, "login"); code != exitOK {
		t.Fatalf("code %d, %q", code, stderr)
	}
	f := r.dev.codeForms[0]
	if f.Get("client_id") != "sagnac-cli" {
		t.Errorf("client_id = %q", f.Get("client_id"))
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(f.Get("device_id")) {
		t.Errorf("device_id = %q", f.Get("device_id"))
	}
	name := f.Get("device_name")
	if name != runtime.GOOS+"-"+runtime.GOARCH || len(name) > 64 {
		t.Errorf("device_name = %q", name)
	}
	if host, _ := os.Hostname(); host != "" && strings.Contains(name, host) {
		t.Errorf("device_name %q carries the host name", name)
	}
}

func TestLoginRespectsInterval(t *testing.T) {
	r := newRig(t, `{"error": "authorization_pending"}`, `{"error": "authorization_pending"}`, "200")
	if code, _, stderr := r.run(t, "login"); code != exitOK {
		t.Fatalf("code %d, %q", code, stderr)
	}
	if fmt.Sprint(r.sleeps) != fmt.Sprint(seconds(5, 5, 5)) {
		t.Errorf("waits = %v, want 5s before each of 3 polls", r.sleeps)
	}
}

func TestSlowDownAdds5s(t *testing.T) {
	r := newRig(t, `{"error": "authorization_pending"}`, `{"error": "slow_down"}`, `{"error": "authorization_pending"}`, "200")
	if code, _, stderr := r.run(t, "login"); code != exitOK {
		t.Fatalf("code %d, %q", code, stderr)
	}
	if fmt.Sprint(r.sleeps) != fmt.Sprint(seconds(5, 5, 10, 10)) {
		t.Errorf("waits = %v, want 5 5 10 10", r.sleeps)
	}
}

func TestLoginExpired(t *testing.T) {
	r := newRig(t, `{"error": "expired_token"}`)
	code, stdout, stderr := r.run(t, "login")
	if code != exitFailure {
		t.Fatalf("code %d, want 1", code)
	}
	if !strings.Contains(stderr, i18n.T("login.expired")) || !strings.Contains(stderr, "sagnac login") {
		t.Errorf("stderr = %q", stderr)
	}
	if tok, _ := r.store.Load(); tok != nil {
		t.Errorf("tokens stored after an expired code: %+v", tok)
	}
	_ = stdout
}

func TestLoginStopsWhenTheCodeExpiresWithoutAnswer(t *testing.T) {
	r := newRig(t, `{"error": "authorization_pending"}`)
	code, _, stderr := r.run(t, "login")
	if code != exitFailure || !strings.Contains(stderr, i18n.T("login.expired")) {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	var total time.Duration
	for _, d := range r.sleeps {
		total += d
	}
	if total > 610*time.Second || r.dev.polls() > 125 {
		t.Errorf("kept polling: %v waited, %d polls", total, r.dev.polls())
	}
}

func TestTooManyDevices(t *testing.T) {
	r := newRig(t, `{"error": "access_denied", "code": "TOO_MANY_DEVICES"}`)
	code, _, stderr := r.run(t, "login")
	if code != exitFailure || !strings.Contains(stderr, r.srv.URL+"/compte/appareils/") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestLoginDenied(t *testing.T) {
	r := newRig(t, `{"error": "access_denied"}`)
	code, _, stderr := r.run(t, "login")
	if code != exitFailure || !strings.Contains(stderr, i18n.T("login.denied")) || strings.Contains(stderr, "compte/appareils") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestLoginKeyringFailedSuggestsSession(t *testing.T) {
	r := newRig(t, "200")
	r.store = &failingStore{}
	code, _, stderr := r.run(t, "login")
	if code != exitInternal || !strings.Contains(stderr, i18n.T("login.keyring_failed")) || !strings.Contains(stderr, "--session") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	for _, secret := range []string{testAccess, testRefresh} {
		if strings.Contains(stderr, secret) {
			t.Errorf("secret printed: %q", stderr)
		}
	}
}

func TestLoginSessionRefused(t *testing.T) {
	r := newRig(t, "200")
	code, stdout, stderr := r.run(t, "login", "--session")
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, i18n.T("login.session_unsupported")) {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if len(r.dev.codeForms) != 0 || len(r.session) != 0 {
		t.Errorf("login --session reached the server or the store")
	}
}

func TestLoginRateLimitedWaitsRetryAfterOnce(t *testing.T) {
	r := newRig(t, "200")
	r.dev.codeStatus = []codeAnswer{{status: 429, retry: "7", body: `{"code": "TOO_MANY_REQUESTS"}`}, {status: 200}}
	code, _, stderr := r.run(t, "login")
	if code != exitOK {
		t.Fatalf("code %d, %q", code, stderr)
	}
	if len(r.sleeps) == 0 || r.sleeps[0] != 7*time.Second || len(r.dev.codeForms) != 2 {
		t.Errorf("waits %v, %d requests", r.sleeps, len(r.dev.codeForms))
	}

	r = newRig(t, "200")
	r.dev.codeStatus = []codeAnswer{{status: 503, retry: "1", body: `{"code": "SERVICE_OVERLOADED"}`}}
	code, _, stderr = r.run(t, "login")
	if code != exitEnvironment || len(r.dev.codeForms) != 2 || stderr == "" {
		t.Errorf("code %d, %d requests, stderr %q", code, len(r.dev.codeForms), stderr)
	}
}

func TestLoginRefusesHostileDeviceCodeAnswer(t *testing.T) {
	bad := map[string]string{
		"escape in uri":   `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "%API%/appareil/\u001b[2J", "expires_in": 600, "interval": 5}`,
		"code in uri":     `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "%API%/appareil/?code=BCDF-GHJK", "expires_in": 600, "interval": 5}`,
		"code in path":    `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "%API%/appareil/BCDFGHJK", "expires_in": 600, "interval": 5}`,
		"other path":      `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "%API%/compte/", "expires_in": 600, "interval": 5}`,
		"other scheme":    `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "javascript:alert(1)", "expires_in": 600, "interval": 5}`,
		"plain http":      `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "http://sagnac.fr/appareil/", "expires_in": 600, "interval": 5}`,
		"user info":       `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "https://sagnac.fr@evil.example/appareil/", "expires_in": 600, "interval": 5}`,
		"escape in code":  `{"device_code": "dc", "user_code": "\u001b[2JBCDF", "verification_uri": "%API%/appareil/", "expires_in": 600, "interval": 5}`,
		"lowercase code":  `{"device_code": "dc", "user_code": "bcdf-ghjk", "verification_uri": "%API%/appareil/", "expires_in": 600, "interval": 5}`,
		"no device code":  `{"device_code": "", "user_code": "BCDF-GHJK", "verification_uri": "%API%/appareil/", "expires_in": 600, "interval": 5}`,
		"zero expires_in": `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "%API%/appareil/", "expires_in": 0, "interval": 5}`,
		"huge interval":   `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "%API%/appareil/", "expires_in": 600, "interval": 99999}`,
		"not json":        `Ligne \u001b[2J`,
	}
	for name, body := range bad {
		r := newRig(t, "200")
		r.dev.codeBody = body
		code, stdout, stderr := r.run(t, "login")
		if code != exitInternal {
			t.Errorf("%s: code %d, want 3 (unexpected answer, A5)", name, code)
		}
		if r.dev.polls() != 0 {
			t.Errorf("%s: token endpoint polled", name)
		}
		if strings.ContainsRune(stdout+stderr, 0x1b) || strings.Contains(stdout+stderr, "javascript") || strings.Contains(stdout+stderr, "evil") {
			t.Errorf("%s: server text reached the terminal: %q %q", name, stdout, stderr)
		}
		if strings.Contains(stdout, "BCDF") {
			t.Errorf("%s: a refused code was shown: %q", name, stdout)
		}
	}
}

func TestServerErrorTextNeverReachesTheTerminal(t *testing.T) {
	r := newRig(t, "\u001b[31m{\"error\": \"Ligne\\u001b[2J\", \"error_description\": \"phrase du serveur\"}")
	r.dev.statuses = []int{400}
	_, stdout, stderr := r.run(t, "login")
	if strings.ContainsRune(stdout+stderr, 0x1b) || strings.Contains(stdout+stderr, "phrase du serveur") {
		t.Errorf("server text shown: %q %q", stdout, stderr)
	}
}

func TestLoginUnreachableServer(t *testing.T) {
	r := newRig(t, "200")
	r.srv.Close()
	code, _, stderr := r.run(t, "login")
	if code != exitEnvironment || !strings.Contains(stderr, r.srv.URL) || strings.Contains(stderr, "dial") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

func TestLoginArguments(t *testing.T) {
	newRig(t, "200")
	if code, _, stderr := execute(t, "login", "extra"); code != exitUsage || !strings.Contains(stderr, "extra") {
		t.Errorf("extra argument: %d %q", code, stderr)
	}
	if code, _, stderr := execute(t, "login", "--bogus"); code != exitUsage || !strings.Contains(stderr, "--bogus") {
		t.Errorf("unknown option: %d %q", code, stderr)
	}
	if code, _, stderr := execute(t, "logout", "--session"); code != exitUsage || !strings.Contains(stderr, "--session") {
		t.Errorf("logout --session: %d %q", code, stderr)
	}
}

func TestLoginRefusesInsecureAPI(t *testing.T) {
	r := newRig(t, "200")
	t.Setenv("SAGNAC_API", "http://example.org")
	code, _, stderr := r.run(t, "login")
	if code != exitFailure || !strings.Contains(stderr, "SAGNAC_API") || len(r.dev.codeForms) != 0 {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

func loggedIn(t *testing.T, r *rig) {
	t.Helper()
	if err := r.store.Save(&account.Tokens{API: r.srv.URL, Access: testAccess, Refresh: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour), Scope: "kits:read"}); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutRevokesAndDeletes(t *testing.T) {
	r := newRig(t, "200")
	loggedIn(t, r)
	code, stdout, stderr := r.run(t, "logout")
	if code != exitOK {
		t.Fatalf("code %d, %q", code, stderr)
	}
	if len(r.dev.revokeForms) != 1 || r.dev.revokeForms[0].Get("token") != testRefresh ||
		r.dev.revokeForms[0].Get("client_id") != "sagnac-cli" {
		t.Errorf("revoke forms = %v", r.dev.revokeForms)
	}
	if tok, err := r.store.Load(); tok != nil || err != nil {
		t.Errorf("tokens kept: %+v, %v", tok, err)
	}
	if !strings.Contains(stdout, i18n.T("logout.done")) || strings.Contains(stdout+stderr, testRefresh) {
		t.Errorf("stdout %q stderr %q", stdout, stderr)
	}
}

func TestLogoutDeletesEvenIfRevocationFails(t *testing.T) {
	r := newRig(t, "200")
	r.dev.revokeCode = 500
	loggedIn(t, r)
	code, _, stderr := r.run(t, "logout")
	if code != exitEnvironment || !strings.Contains(stderr, r.srv.URL+"/compte/appareils/") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if tok, _ := r.store.Load(); tok != nil {
		t.Errorf("tokens kept after a failed revocation: %+v", tok)
	}
}

func TestLogoutWhenNotLoggedIn(t *testing.T) {
	r := newRig(t, "200")
	code, stdout, stderr := r.run(t, "logout")
	if code != exitOK || !strings.Contains(stdout, i18n.T("logout.not_logged_in")) || len(r.dev.revokeForms) != 0 {
		t.Errorf("code %d, stdout %q, stderr %q, %d revocations", code, stdout, stderr, len(r.dev.revokeForms))
	}
	// Tokens of another address are neither sent nor deleted.
	if err := r.store.Save(&account.Tokens{API: "https://other.example", Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := r.run(t, "logout"); code != exitOK || len(r.dev.revokeForms) != 0 {
		t.Errorf("tokens of another address were revoked")
	}
}

func TestLogoutDoesNotFollowRedirects(t *testing.T) {
	r := newRig(t, "200")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("the revocation followed a redirect to %s", req.URL)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	t.Setenv("SAGNAC_API", srv.URL)
	if err := r.store.Save(&account.Tokens{API: srv.URL, Access: "a", Refresh: testRefresh, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	code, _, _ := r.run(t, "logout")
	if code != exitInternal {
		t.Errorf("code %d, want 3 (revocation not confirmed, unexpected answer)", code)
	}
}

func TestLoginRequiredMessageIsTheSame(t *testing.T) {
	var a, b bytes.Buffer
	i18n.Use("fr")
	codeA := reportAccountError(&a, account.ErrNotLoggedIn, "https://sagnac.fr")
	codeB := reportAccountError(&b, account.ErrSessionEnded, "https://sagnac.fr")
	if a.String() != b.String() || a.Len() == 0 || codeA != exitFailure || codeB != exitFailure {
		t.Errorf("%q (%d) vs %q (%d)", a.String(), codeA, b.String(), codeB)
	}
	if !strings.Contains(a.String(), "Connectez-vous avec « sagnac login »") {
		t.Errorf("message = %q", a.String())
	}
}

func TestForeignAddressHasItsOwnMessage(t *testing.T) {
	for _, uri := range []string{"%API%/autre/", "https://sagnac.fr/appareil/", "%API%/appareil"} {
		r := newRig(t, "200")
		r.dev.codeBody = `{"device_code": "dc", "user_code": "BCDF-GHJK", "verification_uri": "` + uri + `", "expires_in": 600, "interval": 5}`
		code, stdout, stderr := r.run(t, "login")
		if code != exitInternal || !strings.Contains(stderr, fmt.Sprintf(i18n.T("login.foreign_address"), r.srv.URL)) || stdout != "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", uri, code, stdout, stderr)
		}
	}
}

func TestPollBusyServerIsWaitedForNotCounted(t *testing.T) {
	r := newRig(t, `{}`, `{}`, `{}`, `{}`, "200")
	r.dev.statuses = []int{429, 503, 429, 429}
	r.dev.retryAfter = map[int]string{0: "30", 1: "9999", 2: "2"}
	code, _, stderr := r.run(t, "login")
	if code != exitOK {
		t.Fatalf("code %d, %q", code, stderr)
	}
	// 30 s asked, 9999 bounded to 120 s, 2 s below the interval, no header: the interval.
	if fmt.Sprint(r.sleeps) != fmt.Sprint(seconds(5, 30, 120, 5, 5)) {
		t.Errorf("waits = %v", r.sleeps)
	}
}

func TestPollDeterministicClientErrorIsNotRetried(t *testing.T) {
	for _, status := range []int{400, 403, 404, 405} {
		r := newRig(t, `{}`)
		r.dev.statuses = []int{status}
		code, _, stderr := r.run(t, "login")
		if code != exitInternal || r.dev.polls() != 1 || stderr == "" {
			t.Errorf("status %d: code %d, %d polls", status, code, r.dev.polls())
		}
	}
}

func TestPollNetworkFailureCounterResetsAndGivesUpAtThird(t *testing.T) {
	r := newRig(t, "ABORT", "ABORT", `{"error": "authorization_pending"}`, "ABORT", "ABORT", "200")
	if code, _, stderr := r.run(t, "login"); code != exitOK || r.dev.polls() != 6 {
		t.Fatalf("two failures, a pending answer, two failures: code %d, %d polls, %q", code, r.dev.polls(), stderr)
	}
	r = newRig(t, "ABORT")
	code, _, stderr := r.run(t, "login")
	if code != exitEnvironment || r.dev.polls() != 3 || !strings.Contains(stderr, r.srv.URL) {
		t.Errorf("code %d, %d polls, stderr %q", code, r.dev.polls(), stderr)
	}
	r = newRig(t, `{}`)
	r.dev.statuses = []int{500}
	if code, _, _ := r.run(t, "login"); code != exitEnvironment || r.dev.polls() != 3 {
		t.Errorf("5xx: code %d, %d polls, want 3", code, r.dev.polls())
	}
}

func ensure(t *testing.T, session bool) (*account.Client, func(), error) {
	t.Helper()
	i18n.Use("fr")
	return ensureTokens(context.Background(), session, &bytes.Buffer{}, &bytes.Buffer{})
}

func TestEnsureTokensUsesStoredLogin(t *testing.T) {
	r := newRig(t, "200")
	loggedIn(t, r)
	client, cleanup, err := ensure(t, false)
	if err != nil || client == nil {
		t.Fatalf("ensureTokens: %v", err)
	}
	cleanup()
	if len(r.dev.codeForms) != 0 || len(r.dev.revokeForms) != 0 {
		t.Errorf("a stored login started a flow or a revocation")
	}
}

func TestEnsureTokensLogsInWhenNothingIsStored(t *testing.T) {
	r := newRig(t, "200")
	client, cleanup, err := ensure(t, false)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if tok, _ := r.store.Load(); tok == nil || tok.Access != testAccess || len(r.dev.revokeForms) != 0 {
		t.Errorf("stored %+v, %d revocations", tok, len(r.dev.revokeForms))
	}
	if tok, err := client.ValidToken(context.Background()); err != nil || tok != testAccess {
		t.Errorf("ValidToken = %q, %v", tok, err)
	}
}

func TestEnsureTokensSessionLeavesStoredLoginAndReleasesItself(t *testing.T) {
	r := newRig(t, "200")
	stored := &account.Tokens{API: r.srv.URL, Access: "sgn_a_PERSISTED", Refresh: "sgn_r_PERSISTED",
		ExpiresAt: time.Now().Add(time.Hour).UTC(), Scope: "kits:read"}
	if err := r.store.Save(stored); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := ensure(t, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.dev.codeForms) != 1 {
		t.Fatalf("a --session run reused the stored login")
	}
	if len(r.dev.revokeForms) != 0 {
		t.Fatal("revoked before the end of the command")
	}
	cleanup()
	if len(r.dev.revokeForms) != 1 || r.dev.revokeForms[0].Get("token") != testRefresh {
		t.Errorf("revocations = %v, want the session's refresh token", r.dev.revokeForms)
	}
	if got, _ := r.store.Load(); got == nil || got.Refresh != "sgn_r_PERSISTED" || got.Access != "sgn_a_PERSISTED" {
		t.Errorf("stored login touched: %+v", got)
	}
	if got, _ := r.sessionStore.Load(); got != nil {
		t.Errorf("session tokens kept after the end: %+v", got)
	}
}

func TestEnsureTokensSessionWritesNothingToDisk(t *testing.T) {
	r := newRig(t, "200")
	openStore = account.OpenStore // the real choice: memory for --session
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	var ids []string
	for range 2 {
		_, cleanup, err := ensure(t, true)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
	}
	for _, f := range r.dev.codeForms {
		ids = append(ids, f.Get("device_id"))
	}
	if len(ids) != 2 || ids[0] == ids[1] || ids[0] == "" {
		t.Errorf("device ids = %v, want two different", ids)
	}
	var found []string
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if p != home {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("--session created %v", found)
	}
}

func TestLoginAndLogoutSendTheUserAgent(t *testing.T) {
	r := newRig(t, "200")
	if code, _, stderr := r.run(t, "login"); code != exitOK {
		t.Fatalf("login: code %d, %q", code, stderr)
	}
	if code, _, stderr := r.run(t, "logout"); code != exitOK {
		t.Fatalf("logout: code %d, %q", code, stderr)
	}
	r.dev.mu.Lock()
	defer r.dev.mu.Unlock()
	if len(r.dev.agents) < 3 {
		t.Fatalf("requests seen: %v", r.dev.agents)
	}
	for _, a := range r.dev.agents {
		if a != "sagnac/"+version {
			t.Errorf("User-Agent = %q, want sagnac/%s", a, version)
		}
	}
}
