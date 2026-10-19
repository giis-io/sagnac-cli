package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// Replaced by the tests: the keyring of the machine and the clock.
var (
	openStore = account.OpenStore
	sleep     = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
)

const (
	deviceGrant       = "urn:ietf:params:oauth:grant-type:device_code"
	slowDownStep      = 5 * time.Second
	maxPollFailures   = 3
	codeTooManyDevice = "TOO_MANY_DEVICES"
)

// parseNoArgs accepts nothing after login or logout. --session is refused on
// login with its own message: it belongs to the commands that need a connection.
func parseNoArgs(command string, args []string, stderr io.Writer) bool {
	for _, a := range args {
		switch {
		case command == "login" && a == "--session":
			fmt.Fprintln(stderr, i18n.T("login.session_unsupported"))
			return false
		case len(a) > 0 && a[0] == '-':
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
			return false
		default:
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, command))
			return false
		}
	}
	return true
}

func runLogin(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !parseNoArgs("login", args, stderr) {
		return exitCodeOf(errUsage)
	}
	apiURL, err := account.URLAPI()
	if err != nil {
		return reportAccountError(stderr, err, "")
	}
	store, err := openStore(apiURL, false, stderr)
	if err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	client, err := newClient(apiURL, store)
	if err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	deviceID, err := account.DeviceID()
	if err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	tokens, err := deviceLogin(ctx, client, deviceID, stdout, stderr)
	if err != nil {
		return reportLoginError(stderr, err, apiURL, false)
	}
	if err := saveLogin(store, tokens, stdout); err != nil {
		if errors.Is(err, account.ErrKeyringFailed) {
			fmt.Fprintln(stderr, i18n.T("login.keyring_failed"))
			return exitCodeOf(err)
		}
		return reportAccountError(stderr, err, apiURL)
	}
	fmt.Fprintln(stdout, i18n.T("login.done"))
	return exitOK
}

// ensureTokens is the entry of every command that needs a connection (pull,
// open, check…). With a stored login it returns the client as is. Without
// one it runs the device flow online. With session, the tokens stay in memory
// for the life of the process, the device identifier is random and written
// nowhere, no configuration directory is created and a stored login is not
// touched; the returned function revokes the family at the end, as well as it
// can, so that the device slot is released. Without session it does nothing.
func ensureTokens(ctx context.Context, session bool, stdout, stderr io.Writer) (*account.Client, func(), error) {
	apiURL, err := account.URLAPI()
	if err != nil {
		return nil, nil, err
	}
	store, err := openStore(apiURL, session, stderr)
	if err != nil {
		return nil, nil, err
	}
	client, err := newClient(apiURL, store)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	if session {
		// Nothing of a --session run touches the disk, the data of the
		// stored login included.
		client.OnTokensDeleted = nil
		cleanup = func() { releaseSession(client, store) }
	}
	_, err = client.ValidToken(ctx)
	if err == nil {
		return client, cleanup, nil
	}
	if !errors.Is(err, account.ErrNotLoggedIn) && !errors.Is(err, account.ErrSessionEnded) {
		return nil, nil, err
	}
	deviceID := account.DeviceID
	var id string
	if session {
		id, err = account.SessionDeviceID()
	} else {
		id, err = deviceID()
	}
	if err != nil {
		return nil, nil, err
	}
	tokens, err := deviceLogin(ctx, client, id, stdout, stderr)
	if err != nil {
		return nil, nil, err
	}
	if session {
		err = store.Save(tokens)
	} else {
		err = saveLogin(store, tokens, stdout)
	}
	if err != nil {
		return nil, nil, err
	}
	return client, cleanup, nil
}

// saveLogin stores the tokens of a new login under a new connection
// identifier, then purges what the device kept for the previous connection,
// whoever it was: the queued attempts and the offline instructions. A login
// that follows an expired or revoked connection finds no tokens to replace,
// so the purge does not depend on them.
func saveLogin(store account.TokenStore, tokens *account.Tokens, w io.Writer) error {
	id, err := account.NewLoginID()
	if err != nil {
		return err
	}
	tokens.Login = id
	if err := store.Save(tokens); err != nil {
		return err
	}
	purgeLocal(w)
	return nil
}

// releaseSession revokes the tokens of a --session run. The context of the
// command may be cancelled (Ctrl-C), so it has its own short one; a failure is
// not reported: the family expires by itself and the person can remove the
// device on the site.
func releaseSession(client *account.Client, store account.TokenStore) {
	tokens, err := store.Load()
	if err != nil || tokens == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = api.Revoke(ctx, client, tokens.Refresh)
	_ = store.Delete()
}

// deviceLogin runs the device flow of RFC 8628: code, instructions, polling.
func deviceLogin(ctx context.Context, client *account.Client, deviceID string, stdout, stderr io.Writer) (*account.Tokens, error) {
	dc, err := requestCode(ctx, client, deviceID, stderr)
	if err != nil {
		return nil, err
	}
	// The address and the code are on two lines, and the address never
	// carries the code: the person types it on the page (RFC 8628 § 5.4).
	fmt.Fprintln(stdout, i18n.T("login.open_url", dc.VerificationURI))
	fmt.Fprintln(stdout, i18n.T("login.enter_code", dc.UserCode))
	fmt.Fprintln(stdout, i18n.T("login.waiting"))
	return pollToken(ctx, client, dc)
}

// requestCode asks for the code; a busy platform (429, 503) is waited for
// once, as long as it says (Retry-After, bounded).
func requestCode(ctx context.Context, c *account.Client, deviceID string, stderr io.Writer) (*api.DeviceCode, error) {
	dc, err := api.RequestDeviceCode(ctx, c, deviceID, account.DeviceName())
	var busy *api.RetryError
	if errors.As(err, &busy) {
		fmt.Fprintln(stderr, i18n.T("login.retrying", int(busy.After/time.Second)))
		if err := sleep(ctx, busy.After); err != nil {
			return nil, err
		}
		dc, err = api.RequestDeviceCode(ctx, c, deviceID, account.DeviceName())
	}
	return dc, err
}

// Codes of the outcomes of the flow; the report maps them to a catalogue message.
const (
	codeExpired = "LOGIN_CODE_EXPIRED"
	codeDenied  = "LOGIN_DENIED"
	codeTooMany = "LOGIN_TOO_MANY_DEVICES"
)

var (
	errExpired = errors.New(codeExpired)
	errDenied  = errors.New(codeDenied)
	errTooMany = errors.New(codeTooMany)
)

// pollToken asks for the tokens every interval until the person validates
// the code (RFC 8628 § 3.5). The wait comes before each request. The time is
// counted in waits, so a stopped clock cannot make the loop endless.
func pollToken(ctx context.Context, c *account.Client, dc *api.DeviceCode) (*account.Tokens, error) {
	interval := dc.Interval
	next := interval
	var waited time.Duration
	failures := 0
	for {
		if waited += next; waited > dc.ExpiresIn {
			return nil, errExpired
		}
		if err := sleep(ctx, next); err != nil {
			return nil, err
		}
		next = interval
		tokens, err := c.PostToken(ctx, map[string][]string{"grant_type": {deviceGrant}, "device_code": {dc.DeviceCode}})
		if err == nil {
			return tokens, nil
		}
		var oauth *account.OAuthError
		var status *account.StatusError
		switch {
		case errors.As(err, &oauth):
			failures = 0
			switch oauth.Err {
			case "authorization_pending":
			case "slow_down":
				interval += slowDownStep
				next = interval
			case "expired_token", "invalid_grant":
				return nil, errExpired
			case "access_denied":
				if oauth.Code == codeTooManyDevice {
					return nil, errTooMany
				}
				return nil, errDenied
			default:
				return nil, err
			}
		case errors.As(err, &status) && (status.Status == http.StatusTooManyRequests || status.Status == http.StatusServiceUnavailable):
			// Busy, not broken: wait as long as asked (bounded), no failure counted.
			next = max(interval, status.RetryAfter)
		case errors.As(err, &status) && status.Status >= 400 && status.Status < 500:
			return nil, err // deterministic: asking again changes nothing
		case errors.Is(err, account.ErrBadTokenResponse) || ctx.Err() != nil:
			return nil, err
		default:
			// Network or 5xx: the code stays valid, try again a few times in a row.
			if failures++; failures >= maxPollFailures {
				return nil, err
			}
		}
	}
}

func reportLoginError(w io.Writer, err error, apiURL string, session bool) int {
	switch {
	case errors.Is(err, errExpired):
		fmt.Fprintln(w, i18n.T("login.expired"))
	case errors.Is(err, errDenied):
		fmt.Fprintln(w, i18n.T("login.denied"))
	case errors.Is(err, api.ErrForeignAddress):
		fmt.Fprintln(w, i18n.T("login.foreign_address", apiURL))
	case errors.Is(err, errTooMany):
		fmt.Fprintln(w, i18n.T("login.too_many_devices", apiURL+"/compte/appareils/"))
	default:
		var busy *api.RetryError
		if errors.As(err, &busy) {
			fmt.Fprintln(w, i18n.T("login.busy"))
			return exitCodeOf(err)
		}
		return reportAccountErrorFor(w, err, apiURL, session)
	}
	return exitCodeOf(err)
}

func runLogout(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !parseNoArgs("logout", args, stderr) {
		return exitCodeOf(errUsage)
	}
	apiURL, err := account.URLAPI()
	if err != nil {
		return reportAccountError(stderr, err, "")
	}
	store, err := openStore(apiURL, false, stderr)
	if err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	tokens, err := store.Load()
	if err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	// Tokens of another address belong to another server: not sent, not deleted.
	if tokens == nil || tokens.API != apiURL {
		fmt.Fprintln(stdout, i18n.T("logout.not_logged_in"))
		return exitOK
	}
	client, err := newClient(apiURL, store)
	if err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	revokeErr := api.Revoke(ctx, client, tokens.Refresh)
	// The local tokens go in every case: the person asked to be disconnected.
	if err := store.Delete(); err != nil {
		return reportAccountError(stderr, err, apiURL)
	}
	purgeLocal(stdout)
	if revokeErr != nil {
		fmt.Fprintln(stderr, i18n.T("logout.revoke_failed", apiURL+"/compte/appareils/"))
		return exitCodeOf(revokeErr)
	}
	fmt.Fprintln(stdout, i18n.T("logout.done"))
	return exitOK
}

// reportAccountError writes the catalogue message of an error of the account
// code and returns its exit code (exitCodeOf). Nothing of the error itself is shown: it
// could carry text of the server. Not logged in and session ended read the
// same, since the person does the same thing.
func reportAccountError(w io.Writer, err error, apiURL string) int {
	return reportAccountErrorFor(w, err, apiURL, false)
}

// reportAccountErrorFor is reportAccountError for a command that may run
// with --session: a connection that ended then has its own message.
func reportAccountErrorFor(w io.Writer, err error, apiURL string, session bool) int {
	var netErr net.Error
	var status *account.StatusError
	var oauth *account.OAuthError
	switch {
	case errors.Is(err, account.ErrNotLoggedIn), errors.Is(err, account.ErrSessionEnded):
		fmt.Fprintln(w, loginAdvice(session))
	case errors.Is(err, account.ErrKeyringFailed):
		fmt.Fprintln(w, i18n.T("error.keyring_failed"))
	case errors.Is(err, account.ErrInsecureURL):
		fmt.Fprintln(w, i18n.T("error.insecure_url"))
	case errors.Is(err, account.ErrNoConfigDir):
		fmt.Fprintln(w, i18n.T("error.no_config_dir"))
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(w, i18n.T("error.interrupted"))
	case errors.Is(err, account.ErrBadTokenResponse), errors.Is(err, api.ErrBadResponse),
		errors.As(err, &status), errors.As(err, &oauth):
		fmt.Fprintln(w, i18n.T("error.unexpected_response"))
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(w, i18n.T("error.unreachable", apiURL))
	default:
		fmt.Fprintln(w, i18n.T("error.storage"))
	}
	return exitCodeOf(err)
}

// newClient is the client of the API for this binary: every request it sends
// says which version of the CLI makes it (User-Agent: sagnac/<version>).
func newClient(apiURL string, store account.TokenStore) (*account.Client, error) {
	c, err := account.NewClient(apiURL, store)
	if err != nil {
		return nil, err
	}
	c.UserAgent = "sagnac/" + version
	// Tokens refused by the server (expired, revoked from the site, reused):
	// what the device kept for that connection goes with them.
	c.OnTokensDeleted = func() { purgeLocal(nil) }
	return c, nil
}
