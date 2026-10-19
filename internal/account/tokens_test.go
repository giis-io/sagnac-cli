package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenServer is a /v1/token of the platform reduced to the refresh grant:
// rotating refresh token, reuse refused with invalid_grant (appareils.jetons).
type tokenServer struct {
	mu        sync.Mutex
	refresh   string
	gen       int
	calls     atomic.Int32
	delay     time.Duration
	lastForm  map[string]string
	protected atomic.Int32
}

func (s *tokenServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/token", func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		time.Sleep(s.delay)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lastForm = map[string]string{}
		for k := range r.PostForm {
			s.lastForm[k] = r.PostForm.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.PostForm.Get("client_id") != "sagnac-cli" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "invalid_client"}`)
			return
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != s.refresh {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error": "invalid_grant"}`)
			s.refresh = "" // reuse closes the family
			return
		}
		s.gen++
		s.refresh = fmt.Sprintf("sgn_r_%d", s.gen)
		fmt.Fprintf(w, `{"access_token": "sgn_a_%d", "token_type": "Bearer", "expires_in": 3600, "refresh_token": %q, "scope": "attempts:write sessions:write kits:read instructions:read"}`, s.gen, s.refresh)
	})
	mux.HandleFunc("/v1/protected", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		want := fmt.Sprintf("Bearer sgn_a_%d", s.gen)
		s.mu.Unlock()
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.protected.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func expired(api string) *Tokens {
	return &Tokens{API: api, Access: "sgn_a_0", Refresh: "sgn_r_0", ExpiresAt: time.Now().Add(-time.Minute),
		Scope: "attempts:write sessions:write kits:read instructions:read"}
}

func TestTransparentRefresh(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_0"}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()

	store := &MemoryStore{}
	if err := store.Save(expired(srv.URL)); err != nil {
		t.Fatal(err)
	}
	c := &Client{API: srv.URL, Store: store, LockPath: filepath.Join(t.TempDir(), "tokens.lock")}

	access, err := c.ValidToken(context.Background())
	if err != nil {
		t.Fatalf("ValidToken: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/protected", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if ts.calls.Load() != 1 || ts.protected.Load() != 1 || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("token calls %d, protected %d (status %d); want 1, 1 (204)", ts.calls.Load(), ts.protected.Load(), resp.StatusCode)
	}
	want := map[string]string{"grant_type": "refresh_token", "refresh_token": "sgn_r_0", "client_id": "sagnac-cli"}
	if fmt.Sprint(ts.lastForm) != fmt.Sprint(want) {
		t.Errorf("form sent = %v, want %v", ts.lastForm, want)
	}
	saved, _ := store.Load()
	if saved.Access != "sgn_a_1" || saved.Refresh != "sgn_r_1" || saved.API != srv.URL ||
		time.Until(saved.ExpiresAt) < 59*time.Minute || saved.Scope != "attempts:write sessions:write kits:read instructions:read" {
		t.Errorf("saved tokens = %+v", saved)
	}

	// A token valid for more than 60 s is used as is.
	if again, err := c.ValidToken(context.Background()); err != nil || again != "sgn_a_1" || ts.calls.Load() != 1 {
		t.Errorf("fresh token: %q, %v, %d calls", again, err, ts.calls.Load())
	}
	// Less than 60 s left: refreshed.
	saved.ExpiresAt = time.Now().Add(30 * time.Second)
	_ = store.Save(saved)
	if again, err := c.ValidToken(context.Background()); err != nil || again != "sgn_a_2" || ts.calls.Load() != 2 {
		t.Errorf("token close to expiry: %q, %v, %d calls", again, err, ts.calls.Load())
	}
}

func TestRefreshRefusedClearsTokens(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_other"} // our refresh token was reused or revoked
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()
	store := &MemoryStore{}
	_ = store.Save(expired(srv.URL))
	c := &Client{API: srv.URL, Store: store}

	if _, err := c.ValidToken(context.Background()); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("ValidToken = %v, want ErrSessionEnded", err)
	}
	if left, _ := store.Load(); left != nil {
		t.Errorf("tokens kept after invalid_grant: %+v", left)
	}
	if _, err := c.ValidToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("after invalid_grant: %v, want ErrNotLoggedIn", err)
	}
}

// Tokens refused by the server: the hook of the commands runs once the
// tokens are gone (the queue and the offline copies are purged there).
func TestRefreshRefusedCallsOnTokensDeleted(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_other"}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()
	store := &MemoryStore{}
	_ = store.Save(expired(srv.URL))
	calls := 0
	c := &Client{API: srv.URL, Store: store, OnTokensDeleted: func() {
		calls++
		if left, _ := store.Load(); left != nil {
			t.Error("hook called before the tokens were deleted")
		}
	}}
	if _, err := c.ValidToken(context.Background()); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("ValidToken = %v, want ErrSessionEnded", err)
	}
	if calls != 1 {
		t.Errorf("hook called %d times, want 1", calls)
	}
}

// A refresh keeps the connection identifier: the queued attempts of this
// connection stay sendable.
func TestRefreshKeepsLogin(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_0"}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()
	store := &MemoryStore{}
	tok := expired(srv.URL)
	tok.Login = "3e6c4f0b-7d8a-4c5e-8f4b-5a6c7d8e9f0a"
	_ = store.Save(tok)
	c := &Client{API: srv.URL, Store: store, OnTokensDeleted: func() { t.Error("hook called on a refresh") }}
	if _, err := c.ValidToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if saved, _ := store.Load(); saved.Access != "sgn_a_1" || saved.Login != tok.Login {
		t.Errorf("saved %+v", saved)
	}
}

// Tokens stored before the identifier existed get one, kept from then on.
func TestLoginIDDrawnOnceForOldTokens(t *testing.T) {
	store := &MemoryStore{}
	tok := expired("https://sagnac.example")
	tok.ExpiresAt = time.Now().Add(time.Hour)
	_ = store.Save(tok)
	c := &Client{API: "https://sagnac.example", Store: store}
	first, err := c.LoginID(context.Background())
	if err != nil || !reDeviceID.MatchString(first) {
		t.Fatalf("LoginID = %q, %v", first, err)
	}
	if again, _ := c.LoginID(context.Background()); again != first {
		t.Errorf("second LoginID %q, want %q", again, first)
	}
	if saved, _ := store.Load(); saved.Login != first || saved.Access != tok.Access {
		t.Errorf("saved %+v", saved)
	}
	if _, err := (&Client{API: "https://autre.example", Store: store}).LoginID(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("tokens of another server: %v", err)
	}
}

func TestRefreshFailureKeepsTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	store := &MemoryStore{}
	_ = store.Save(expired(srv.URL))
	c := &Client{API: srv.URL, Store: store}
	if _, err := c.ValidToken(context.Background()); err == nil || errors.Is(err, ErrSessionEnded) {
		t.Fatalf("503: ValidToken = %v, want a temporary error", err)
	}
	srv.Close()
	if _, err := c.ValidToken(context.Background()); err == nil || errors.Is(err, ErrSessionEnded) {
		t.Fatalf("server down: ValidToken = %v, want a temporary error", err)
	}
	if left, _ := store.Load(); left == nil || left.Refresh != "sgn_r_0" {
		t.Errorf("tokens lost after a network failure: %+v", left)
	}
}

func TestBadTokenResponseRefused(t *testing.T) {
	bodies := []string{
		`{"access_token": "", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "r"}`,
		`{"access_token": "a", "token_type": "mac", "expires_in": 3600, "refresh_token": "r"}`,
		`{"access_token": "a", "token_type": "Bearer", "expires_in": 0, "refresh_token": "r"}`,
		`{"access_token": "a", "token_type": "Bearer", "expires_in": 3600, "refresh_token": ""}`,
		`{"access_token": "a", "token_type": "Bearer", "expires_in": 9223372036854775807, "refresh_token": "r"}`,
		`{"access_token": "a\nb", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "r"}`,
		`not json`,
	}
	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		store := &MemoryStore{}
		_ = store.Save(expired(srv.URL))
		c := &Client{API: srv.URL, Store: store}
		if _, err := c.ValidToken(context.Background()); !errors.Is(err, ErrBadTokenResponse) {
			t.Errorf("%s: ValidToken = %v, want ErrBadTokenResponse", body, err)
		}
		srv.Close()
	}
}

func TestTokensOfAnotherServerNotSent(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_0"}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()
	store := &MemoryStore{}
	_ = store.Save(expired("https://sagnac.fr"))
	c := &Client{API: srv.URL, Store: store}
	if _, err := c.ValidToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("ValidToken = %v, want ErrNotLoggedIn", err)
	}
	if ts.calls.Load() != 0 {
		t.Errorf("refresh token of https://sagnac.fr sent to %s", srv.URL)
	}
}

func TestNotLoggedIn(t *testing.T) {
	c := &Client{API: "https://sagnac.fr", Store: &MemoryStore{}}
	if _, err := c.ValidToken(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("ValidToken = %v, want ErrNotLoggedIn", err)
	}
}

// TestTwoProcessesOneRefresh: two CLIs find the access
// token expired at the same time; the server receives one refresh request
// and both commands get the new access token.
func TestTwoProcessesOneRefresh(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_0", delay: 300 * time.Millisecond}
	srv := httptest.NewServer(ts.handler(t))
	defer srv.Close()

	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens.json")
	if err := NewFileStore(tokens, nil).Save(expired(srv.URL)); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(500 * time.Millisecond).UnixNano()

	outs := make([]bytes.Buffer, 2)
	cmds := make([]*exec.Cmd, 2)
	for i := range cmds {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRefreshProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			"SAGNAC_TEST_HELPER=1",
			"SAGNAC_TEST_API="+srv.URL,
			"SAGNAC_TEST_TOKENS="+tokens,
			"SAGNAC_TEST_LOCK="+filepath.Join(dir, "tokens.lock"),
			fmt.Sprintf("SAGNAC_TEST_START=%d", start),
		)
		cmd.Stdout = &outs[i]
		cmd.Stderr = &outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child %d: %v\n%s", i, err, outs[i].String())
		}
	}
	if n := ts.calls.Load(); n != 1 {
		t.Fatalf("server received %d refresh requests, want 1", n)
	}
	for i := range outs {
		if !strings.Contains(outs[i].String(), "ACCESS=sgn_a_1\n") {
			t.Errorf("child %d did not get the new access token:\n%s", i, outs[i].String())
		}
	}
	saved, err := NewFileStore(tokens, nil).Load()
	if err != nil || saved.Refresh != "sgn_r_1" {
		t.Errorf("stored tokens = %+v, %v", saved, err)
	}
}

// TestHelperRefreshProcess is the child of TestTwoProcessesOneRefresh.
func TestHelperRefreshProcess(t *testing.T) {
	if os.Getenv("SAGNAC_TEST_HELPER") != "1" {
		t.Skip("child process of TestTwoProcessesOneRefresh")
	}
	var start int64
	fmt.Sscan(os.Getenv("SAGNAC_TEST_START"), &start)
	time.Sleep(time.Until(time.Unix(0, start)))
	c := &Client{
		API:      os.Getenv("SAGNAC_TEST_API"),
		Store:    NewFileStore(os.Getenv("SAGNAC_TEST_TOKENS"), nil),
		LockPath: os.Getenv("SAGNAC_TEST_LOCK"),
	}
	access, err := c.ValidToken(context.Background())
	if err != nil {
		fmt.Println("ERROR", err)
		os.Exit(1)
	}
	fmt.Printf("ACCESS=%s\n", access)
}

func TestLockHonoursContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.lock")
	unlock, err := LockFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Same process, other descriptor: flock locks are per open file, so this waits.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := LockFile(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second lock = %v, want context.DeadlineExceeded", err)
	}
	assertMode(t, path, 0o600)
}

func TestFileMode0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sagnac")
	path := filepath.Join(dir, "tokens.json")
	var notice bytes.Buffer
	store := NewFileStore(path, &notice)

	if got, err := store.Load(); got != nil || err != nil {
		t.Fatalf("Load on empty store = %+v, %v", got, err)
	}
	tok := expired("https://sagnac.fr")
	if err := store.Save(tok); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
	assertMode(t, dir, 0o700)
	if !strings.Contains(notice.String(), path) || !strings.Contains(notice.String(), "--session") {
		t.Errorf("file store not announced with its path and --session: %q", notice.String())
	}

	// The notice is shown when the file is created, not at each refresh.
	notice.Reset()
	tok.Access = "sgn_a_9"
	if err := store.Save(tok); err != nil {
		t.Fatal(err)
	}
	if notice.Len() != 0 {
		t.Errorf("notice repeated: %q", notice.String())
	}

	// A file loosened by hand is written back 0600.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(tok); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)

	got, err := store.Load()
	if err != nil || got.Access != "sgn_a_9" || got.Refresh != "sgn_r_0" || !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Errorf("Load = %+v, %v", got, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Delete left the file: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Errorf("Delete twice: %v", err)
	}

	// No temporary file is left next to the tokens.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("left in %s: %s", dir, e.Name())
	}
}

func TestDamagedTokenFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path, nil).Load(); err == nil {
		t.Error("damaged token file loaded without error")
	}
}

func TestMemoryStoreWritesNothing(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	store, err := OpenStore("https://sagnac.fr", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*MemoryStore); !ok {
		t.Fatalf("--session store is %T, want *MemoryStore", store)
	}
	_ = store.Save(expired("https://sagnac.fr"))
	c, err := NewClient("https://sagnac.fr", store)
	if err != nil {
		t.Fatal(err)
	}
	if c.LockPath != "" {
		t.Errorf("memory store uses a lock file %q", c.LockPath)
	}
	entries, _ := os.ReadDir(xdg)
	if len(entries) != 0 {
		t.Errorf("--session wrote %v", entries)
	}
}

func TestTokensJSONHasNoUnexpectedFields(t *testing.T) {
	raw, _ := json.Marshal(expired("https://sagnac.fr"))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"api", "access_token", "refresh_token", "expires_at", "scope"} {
		if _, ok := m[k]; !ok {
			t.Errorf("field %q missing from %s", k, raw)
		}
	}
	if len(m) != 5 {
		t.Errorf("unexpected fields in %s", raw)
	}
}

// postToken sends form to a /v1/token that answers status and body.
func postToken(t *testing.T, form url.Values, status int, body string, delay time.Duration) (*Tokens, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c := &Client{API: srv.URL, Store: &MemoryStore{}}
	return c.PostToken(context.Background(), form)
}

func tokenBody(expires int, scope string) string {
	return fmt.Sprintf(`{"access_token": "sgn_a_1", "token_type": "Bearer", "expires_in": %d, "refresh_token": "sgn_r_1", "scope": %q}`, expires, scope)
}

func TestExpiresInMustExceed120(t *testing.T) {
	for _, n := range []int{-5, 0, 1, 60, 120} {
		if _, err := postToken(t, url.Values{}, 200, tokenBody(n, "kits:read"), 0); !errors.Is(err, ErrBadTokenResponse) {
			t.Errorf("expires_in %d: err = %v, want ErrBadTokenResponse", n, err)
		}
	}
	if _, err := postToken(t, url.Values{}, 200, tokenBody(121, "kits:read"), 0); err != nil {
		t.Errorf("expires_in 121: %v", err)
	}
}

func TestScopeIsBounded(t *testing.T) {
	bad := []string{"kits:read\x1b[2J", "KITS:READ", "kits:read\nattempts:write", "a1", strings.Repeat("a", 257)}
	for _, s := range bad {
		if _, err := postToken(t, url.Values{}, 200, tokenBody(3600, s), 0); !errors.Is(err, ErrBadTokenResponse) {
			t.Errorf("scope %q: err = %v, want ErrBadTokenResponse", s, err)
		}
	}
	for _, s := range []string{"", "kits:read instructions:read", strings.Repeat("a", 256)} {
		if _, err := postToken(t, url.Values{}, 200, tokenBody(3600, s), 0); err != nil {
			t.Errorf("scope %q refused: %v", s, err)
		}
	}
}

func TestOAuthErrorIsFiltered(t *testing.T) {
	_, err := postToken(t, url.Values{}, 400, `{"error": "authorization_pending"}`, 0)
	var oauth *OAuthError
	if !errors.As(err, &oauth) || oauth.Err != "authorization_pending" {
		t.Fatalf("err = %v, want OAuthError authorization_pending", err)
	}
	for _, e := range []string{"Ligne\\u001b[2Jfausse", "UPPER", "with space", strings.Repeat("a", 65), "a-b"} {
		_, err := postToken(t, url.Values{}, 400, `{"error": "`+e+`"}`, 0)
		var status *StatusError
		if errors.As(err, &oauth) || !errors.As(err, &status) || status.Status != 400 {
			t.Errorf("error %q: err = %v, want StatusError 400", e, err)
		}
	}
	_, err = postToken(t, url.Values{}, 400, `{"error": "access_denied", "code": "TOO_MANY_DEVICES"}`, 0)
	if !errors.As(err, &oauth) || oauth.Code != "TOO_MANY_DEVICES" {
		t.Errorf("code lost: %v", err)
	}
	_, err = postToken(t, url.Values{}, 400, `{"error": "access_denied", "code": "x\u001b[2J"}`, 0)
	if !errors.As(err, &oauth) || oauth.Code != "" {
		t.Errorf("unfiltered code: %+v", oauth)
	}
}

func TestPostTokenDoesNotMutateForm(t *testing.T) {
	form := url.Values{"grant_type": {"refresh_token"}}
	if _, err := postToken(t, form, 200, tokenBody(3600, ""), 0); err != nil {
		t.Fatal(err)
	}
	if len(form) != 1 || form.Get("client_id") != "" {
		t.Errorf("caller's form modified: %v", form)
	}
}

func TestExpiryCountsFromBeforeTheRequest(t *testing.T) {
	start := time.Now()
	tok, err := postToken(t, url.Values{}, 200, tokenBody(3600, ""), 400*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if late := tok.ExpiresAt.Sub(start.Add(time.Hour)); late > 100*time.Millisecond {
		t.Errorf("expiry %v after start+expires_in: the clock was read after the response", late)
	}
}

// replayServer answers /v1/p: 204 for the bearer in want, else status/body.
type replayServer struct {
	*tokenServer
	srv  *httptest.Server
	mu2  sync.Mutex
	want string
	deny string // body of the refusal
	hits []string
}

func newReplayServer(t *testing.T, ts *tokenServer, want, deny string) *replayServer {
	t.Helper()
	r := &replayServer{tokenServer: ts, want: want, deny: deny}
	mux := http.NewServeMux()
	mux.Handle("/v1/token", ts.handler(t))
	mux.HandleFunc("/v1/p", func(w http.ResponseWriter, req *http.Request) {
		r.mu2.Lock()
		r.hits = append(r.hits, req.Header.Get("Authorization"))
		r.mu2.Unlock()
		if req.Header.Get("Authorization") == "Bearer "+r.want {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, r.deny)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *replayServer) client(t *testing.T, tokens *Tokens) (*Client, *MemoryStore) {
	t.Helper()
	store := &MemoryStore{}
	tokens.API = r.srv.URL
	if err := store.Save(tokens); err != nil {
		t.Fatal(err)
	}
	return &Client{API: r.srv.URL, Store: store}, store
}

func getP(c *Client) func(context.Context) (*http.Response, error) {
	return func(ctx context.Context) (*http.Response, error) {
		return c.DoAuthorized(ctx, nil, func(token string) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+"/v1/p", nil)
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			return req, err
		})
	}
}

func liveTokens(access string) *Tokens {
	return &Tokens{Access: access, Refresh: "sgn_r_0", ExpiresAt: time.Now().Add(time.Hour), Scope: "kits:read"}
}

// 401 INVALID_TOKEN on a token the clock still believes: one forced refresh,
// the request is replayed once.
func TestInvalidTokenForcesOneRefreshAndReplays(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_0"}
	r := newReplayServer(t, ts, "sgn_a_1", `{"code": "INVALID_TOKEN"}`)
	c, store := r.client(t, liveTokens("sgn_a_0"))
	resp, err := getP(c)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || ts.calls.Load() != 1 || fmt.Sprint(r.hits) != "[Bearer sgn_a_0 Bearer sgn_a_1]" {
		t.Errorf("status %d, refreshes %d, hits %v", resp.StatusCode, ts.calls.Load(), r.hits)
	}
	if saved, _ := store.Load(); saved.Access != "sgn_a_1" {
		t.Errorf("saved %+v", saved)
	}
}

// Replayed once only: a second refusal is handed back, whole.
func TestInvalidTokenReplayedOnce(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_0"}
	r := newReplayServer(t, ts, "never", `{"code": "INVALID_TOKEN"}`)
	c, _ := r.client(t, liveTokens("sgn_a_0"))
	resp, err := getP(c)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 401 || !strings.Contains(string(body), "INVALID_TOKEN") || len(r.hits) != 2 || ts.calls.Load() != 1 {
		t.Errorf("status %d, body %q, hits %v, refreshes %d", resp.StatusCode, body, r.hits, ts.calls.Load())
	}
}

// Another code, or a 401 without a code, is not a reason to refresh.
func TestOtherRefusalsNotReplayed(t *testing.T) {
	for _, deny := range []string{`{"code": "ENTITLEMENT_INACTIVE"}`, ``, `not json`} {
		ts := &tokenServer{refresh: "sgn_r_0"}
		r := newReplayServer(t, ts, "never", deny)
		c, _ := r.client(t, liveTokens("sgn_a_0"))
		resp, err := getP(c)(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != deny || len(r.hits) != 1 || ts.calls.Load() != 0 {
			t.Errorf("%q: body %q, hits %v, refreshes %d", deny, body, r.hits, ts.calls.Load())
		}
	}
}

// A second CLI refreshed meanwhile: its tokens are used, the refresh token
// is not spent again.
func TestInvalidTokenUsesTokensRefreshedByAnotherProcess(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_9"}
	r := newReplayServer(t, ts, "sgn_a_9", `{"code": "INVALID_TOKEN"}`)
	c, store := r.client(t, liveTokens("sgn_a_0"))
	stale := true
	// The other process stores its tokens between our first and second look.
	c.Store = &swapStore{TokenStore: store, after: func() {
		if stale {
			stale = false
			_ = store.Save(&Tokens{API: r.srv.URL, Access: "sgn_a_9", Refresh: "sgn_r_9", ExpiresAt: time.Now().Add(time.Hour)})
		}
	}}
	resp, err := getP(c)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || ts.calls.Load() != 0 {
		t.Errorf("status %d, refreshes %d, hits %v", resp.StatusCode, ts.calls.Load(), r.hits)
	}
}

// swapStore runs after() once the first Load is done.
type swapStore struct {
	TokenStore
	n     int
	after func()
}

func (s *swapStore) Load() (*Tokens, error) {
	t, err := s.TokenStore.Load()
	if s.n++; s.n == 1 {
		defer s.after()
	}
	return t, err
}

// The refresh token itself refused: the session is over, the request is not replayed.
func TestInvalidTokenWithDeadFamilyEndsSession(t *testing.T) {
	ts := &tokenServer{refresh: "sgn_r_other"}
	r := newReplayServer(t, ts, "never", `{"code": "INVALID_TOKEN"}`)
	c, store := r.client(t, liveTokens("sgn_a_0"))
	if _, err := getP(c)(context.Background()); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("err = %v", err)
	}
	if left, _ := store.Load(); left != nil || len(r.hits) != 1 {
		t.Errorf("tokens %+v, hits %v", left, r.hits)
	}
}
