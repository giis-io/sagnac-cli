package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/giis-io/sagnac-cli/internal/i18n"
)

var (
	// ErrNotLoggedIn: no tokens for this API address; the command asks for
	// "sagnac login".
	ErrNotLoggedIn = errors.New(codeNotLoggedIn)
	// ErrSessionEnded: the server refused the refresh token (invalid_grant:
	// revoked, reused, expired family). The local tokens are deleted.
	ErrSessionEnded = errors.New(codeSessionEnded)
	// ErrBadTokenResponse: a 200 from /v1/token that is not a usable token.
	ErrBadTokenResponse = errors.New(codeBadResponse)
)

const (
	clientID         = "sagnac-cli"
	codeInvalidToken = "INVALID_TOKEN"
	refreshMargin    = 60 * time.Second
	httpTimeout      = 30 * time.Second
	maxBody          = 64 << 10
	// expires_in (the server gives 3600) is bounded before it becomes a
	// Duration: under 2 minutes the 60 s margin would refresh at every call.
	minExpiresIn = 120
	maxExpiresIn = 24 * 3600
)

// Tokens are the tokens of one API address, as stored.
type Tokens struct {
	API       string    `json:"api"`
	Access    string    `json:"access_token"`
	Refresh   string    `json:"refresh_token"`
	ExpiresAt time.Time `json:"expires_at"`
	Scope     string    `json:"scope"`
	// Login identifies this connection of the device: a UUID drawn at each
	// successful login and kept across refreshes. The queue marks every
	// entry with it, so that an attempt is never sent under another login.
	Login string `json:"login,omitempty"`
}

// TokenStore keeps the tokens. Load returns nil, nil when nothing is stored;
// Delete of nothing is not an error.
type TokenStore interface {
	Load() (*Tokens, error)
	Save(*Tokens) error
	Delete() error
}

// OpenStore picks where the tokens of api live:
// memory only with --session (nothing written to disk), else the keyring of
// the system (macOS Keychain through /usr/bin/security, Secret Service
// through secret-tool), else the file $XDG_CONFIG_HOME/sagnac/tokens.json
// (0600), announced on notice when it is created.
func OpenStore(api string, session bool, notice io.Writer) (TokenStore, error) {
	if session {
		return &MemoryStore{}, nil
	}
	switch runtime.GOOS {
	case "darwin":
		if keychainAvailable() {
			return &KeychainStore{Tool: securityTool, Account: api}, nil
		}
	case "linux":
		if tool, ok := secretServiceAvailable(); ok {
			return &SecretServiceStore{Tool: tool, Account: api}, nil
		}
	}
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	return NewFileStore(filepath.Join(dir, "tokens.json"), notice), nil
}

// MemoryStore keeps the tokens for the life of the process (--session).
type MemoryStore struct {
	mu sync.Mutex
	t  *Tokens
}

func (m *MemoryStore) Load() (*Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.t == nil {
		return nil, nil
	}
	c := *m.t
	return &c, nil
}

func (m *MemoryStore) Save(t *Tokens) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *t
	m.t = &c
	return nil
}

func (m *MemoryStore) Delete() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.t = nil
	return nil
}

// FileStore keeps the tokens in a JSON file of mode 0600, in a directory of
// mode 0700, replaced atomically. It is the last resort when there is no
// keyring: any process of the same user can read the file.
type FileStore struct {
	Path   string
	notice io.Writer
}

// NewFileStore returns the file store of path; the first Save that creates
// the file writes the catalogue notice on notice (nil: silent).
func NewFileStore(path string, notice io.Writer) *FileStore {
	return &FileStore{Path: path, notice: notice}
}

func (f *FileStore) Load() (*Tokens, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (f *FileStore) Save(t *Tokens) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_, statErr := os.Lstat(f.Path)
	created := errors.Is(statErr, os.ErrNotExist)
	tmp, err := writeTemp(dir, "tokens", raw)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if created && f.notice != nil {
		fmt.Fprintln(f.notice, i18n.T("account.file_store_notice", f.Path))
	}
	return nil
}

func (f *FileStore) Delete() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// OAuthError is an OAuth error of /v1/token (docs/api/erreurs.md): Err is
// the "error" field, Code the optional "code" of the list.
type OAuthError struct {
	Status int
	Err    string
	Code   string
}

func (e *OAuthError) Error() string { return "OAUTH_" + strings.ToUpper(e.Err) }

// StatusError: /v1/token answered a status that is neither 200 nor an OAuth
// error (proxy, server error). The tokens are kept.
// RetryAfter is the bounded Retry-After of a 429 or 503 (0: absent).
type StatusError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return "ACCOUNT_HTTP_" + strconv.Itoa(e.Status) }

// Client gets a valid access token for API from Store.
type Client struct {
	API   string
	Store TokenStore
	// HTTP is the client used for /v1/token; nil: 30 s timeout, no redirect.
	HTTP *http.Client
	// LockPath is the file locked around a refresh; empty for a memory
	// store, which only needs the in-process mutex.
	LockPath string
	// UserAgent is sent with every request of the client (sagnac/<version>);
	// empty: Go's default.
	UserAgent string
	// OnTokensDeleted is called after the stored tokens were deleted because
	// the server refused them (invalid_grant: expired, revoked, reused). The
	// commands purge there what belongs to the ended connection.
	OnTokensDeleted func()

	mu sync.Mutex
}

// NewClient returns the client of api over store. Except for a memory
// store, refreshes are serialized by $XDG_CONFIG_HOME/sagnac/tokens.lock.
func NewClient(api string, store TokenStore) (*Client, error) {
	c := &Client{API: api, Store: store}
	if _, memory := store.(*MemoryStore); !memory {
		dir, err := ensureConfigDir()
		if err != nil {
			return nil, err
		}
		c.LockPath = filepath.Join(dir, "tokens.lock")
	}
	return c, nil
}

// ValidToken returns an access token valid for at least 60 s. A token that
// expires sooner is refreshed under the lock: after taking it, the tokens
// are read again, so a second CLI that waited uses the tokens the first one
// got instead of spending the refresh token again (which the server would
// take for a reuse and answer by closing the family).
func (c *Client) ValidToken(ctx context.Context) (string, error) {
	t, err := c.current()
	if err != nil {
		return "", err
	}
	if fresh(t) {
		return t.Access, nil
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	if t, err = c.current(); err != nil {
		return "", err
	}
	if fresh(t) {
		return t.Access, nil
	}
	return c.refresh(ctx, t)
}

// refresh spends the refresh token of t, with the lock held. The server has
// already consumed the old refresh token when the answer comes: if the Save
// fails, the next refresh is refused and a new login is needed.
func (c *Client) refresh(ctx context.Context, t *Tokens) (string, error) {
	next, err := c.PostToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	var oauth *OAuthError
	if errors.As(err, &oauth) && oauth.Err == "invalid_grant" {
		if err := c.Store.Delete(); err != nil {
			return "", err
		}
		if c.OnTokensDeleted != nil {
			c.OnTokensDeleted()
		}
		return "", ErrSessionEnded
	}
	if err != nil {
		return "", err
	}
	next.Login = t.Login
	if err := c.Store.Save(next); err != nil {
		return "", err
	}
	return next.Access, nil
}

// refreshAfterRefusal is the refresh forced by a 401 INVALID_TOKEN on the
// token stale: the clock may still believe it (revoked, device closed). If
// another process stored other, still valid tokens meanwhile, those are used
// and the refresh token is not spent again.
func (c *Client) refreshAfterRefusal(ctx context.Context, stale string) (string, error) {
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	t, err := c.current()
	if err != nil {
		return "", err
	}
	if t.Access != stale && fresh(t) {
		return t.Access, nil
	}
	return c.refresh(ctx, t)
}

// DoAuthorized sends the request build makes with a valid access token. On a
// 401 whose code is INVALID_TOKEN it forces one refresh and sends the
// request again, once; whatever comes back then is returned as is. Any other
// answer is returned untouched, its body readable. hc nil: the client of the
// account (timeout, no redirect). build is called again for the replay, so a
// body must be made anew each time.
func (c *Client) DoAuthorized(ctx context.Context, hc *http.Client, build func(token string) (*http.Request, error)) (*http.Response, error) {
	if hc == nil {
		hc = c.HTTPClient()
	}
	token, err := c.ValidToken(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(hc, build, token)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	resp.Body.Close()
	var body struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Code != codeInvalidToken {
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		return resp, nil
	}
	token, err = c.refreshAfterRefusal(ctx, token)
	if err != nil {
		return nil, err
	}
	return c.send(hc, build, token)
}

func (c *Client) send(hc *http.Client, build func(string) (*http.Request, error), token string) (*http.Response, error) {
	req, err := build(token)
	if err != nil {
		return nil, err
	}
	c.SetUserAgent(req)
	return hc.Do(req)
}

func (c *Client) current() (*Tokens, error) {
	t, err := c.Store.Load()
	if err != nil {
		return nil, err
	}
	// Tokens of another address are never sent here (SAGNAC_API changed).
	if t == nil || t.API != c.API || t.Refresh == "" {
		return nil, ErrNotLoggedIn
	}
	return t, nil
}

// NewLoginID draws the identifier of a new connection (Tokens.Login).
func NewLoginID() (string, error) { return newUUID() }

// LoginID returns the identifier of the stored connection. Tokens stored by
// an older version have none: one is drawn and stored, under the lock.
func (c *Client) LoginID(ctx context.Context) (string, error) {
	t, err := c.current()
	if err != nil {
		return "", err
	}
	if t.Login != "" {
		return t.Login, nil
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	if t, err = c.current(); err != nil {
		return "", err
	}
	if t.Login != "" {
		return t.Login, nil
	}
	if t.Login, err = NewLoginID(); err != nil {
		return "", err
	}
	if err := c.Store.Save(t); err != nil {
		return "", err
	}
	return t.Login, nil
}

func fresh(t *Tokens) bool {
	return t.Access != "" && time.Until(t.ExpiresAt) > refreshMargin
}

func (c *Client) lock(ctx context.Context) (func(), error) {
	if c.LockPath == "" {
		c.mu.Lock()
		return c.mu.Unlock, nil
	}
	return LockFile(ctx, c.LockPath)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout: httpTimeout,
		// A 307 or 308 would send the refresh token again, to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Code         string `json:"code"`
}

var (
	reToken = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
	// The scope is stored and kept: only the scope names of the server.
	reScope = regexp.MustCompile(`^[a-z:_ ]{0,256}$`)
	// What reaches an OAuthError, hence a switch of the commands, never raw text.
	reOAuthError = regexp.MustCompile(`^[a-z_]{1,64}$`)
	reOAuthCode  = regexp.MustCompile(`^[A-Z_]{1,64}$`)
)

// HTTPClient is the client used for the calls to the API: timeout, and no
// redirect (a 307 or 308 would send a secret again, to another host).
func (c *Client) HTTPClient() *http.Client { return c.httpClient() }

// SetUserAgent puts the User-Agent of the CLI on a request to the API.
func (c *Client) SetUserAgent(req *http.Request) {
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
}

// PostToken sends form to /v1/token with client_id and returns the new
// tokens. An OAuth error comes back as *OAuthError (authorization_pending,
// slow_down and the others of the device flow included).
func (c *Client) PostToken(ctx context.Context, form url.Values) (*Tokens, error) {
	// The expiry counts from the request: a slow answer must not extend it.
	sent := time.Now()
	body := url.Values{}
	for k, v := range form {
		body[k] = append([]string(nil), v...)
	}
	body.Set("client_id", clientID)
	form = body
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+"/v1/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	c.SetUserAgent(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var parsed tokenResponse
	jsonErr := json.Unmarshal(raw, &parsed)
	switch {
	case resp.StatusCode == http.StatusOK:
		if jsonErr != nil || !strings.EqualFold(parsed.TokenType, "Bearer") ||
			parsed.ExpiresIn <= minExpiresIn || parsed.ExpiresIn > maxExpiresIn ||
			!reToken.MatchString(parsed.AccessToken) || !reToken.MatchString(parsed.RefreshToken) ||
			!reScope.MatchString(parsed.Scope) {
			return nil, ErrBadTokenResponse
		}
		return &Tokens{
			API:       c.API,
			Access:    parsed.AccessToken,
			Refresh:   parsed.RefreshToken,
			ExpiresAt: sent.Add(time.Duration(parsed.ExpiresIn) * time.Second).UTC(),
			Scope:     parsed.Scope,
		}, nil
	case (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) &&
		jsonErr == nil && reOAuthError.MatchString(parsed.Error):
		code := parsed.Code
		if !reOAuthCode.MatchString(code) {
			code = ""
		}
		return nil, &OAuthError{Status: resp.StatusCode, Err: parsed.Error, Code: code}
	default:
		return nil, &StatusError{Status: resp.StatusCode, RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"))}
	}
}

// MaxRetryAfter bounds what a server can make the CLI wait.
const MaxRetryAfter = 120 * time.Second

// ParseRetryAfter reads a Retry-After header in seconds (the only form the
// platform sends): 0 when absent or unreadable, otherwise at most
// MaxRetryAfter. The callers choose their own fallback for 0.
func ParseRetryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n < 1 {
		return 0
	}
	return min(time.Duration(n)*time.Second, MaxRetryAfter)
}
