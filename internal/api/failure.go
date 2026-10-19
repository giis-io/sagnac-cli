package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
)

// The error contract of the API (docs/api/erreurs.md) as the authenticated
// calls of the CLI read it.
const (
	codeAPIPrefix   = "API_CODE_"
	codeEntitlement = "ENTITLEMENT_INACTIVE"
	codeCLITooOld   = "CLI_TOO_OLD"
)

var (
	reCode    = regexp.MustCompile(`^[A-Z_]{1,64}$`)
	reState   = regexp.MustCompile(`^[a-z]{1,32}$`)
	rePageURL = regexp.MustCompile(`^[\x21-\x7e]{1,1000}$`)
	// A version as SAGNAC_CLI gives it: digits, dots, letters of a pre-release.
	reMinimum = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,32}$`)
)

// APIError is an error of the list of docs/api/erreurs.md. Code and State
// are checked against a pattern: no text of the server reaches a message.
//
// Minimum is the field "minimum" of CLI_TOO_OLD, kept only when it reads as
// a version.
//
// URL is the field "url" of an answer, kept only when it is printable ASCII
// of a reasonable length (1000); it is not trusted: whoever uses it checks it.
//
// RetryAfter is the Retry-After of the answer (429 TOO_MANY_REQUESTS), in
// whole seconds, at most MaxDelay; 0 when absent or unreadable.
type APIError struct {
	Status     int
	Code       string
	State      string
	URL        string
	Minimum    string
	RetryAfter time.Duration
}

// MaxDelay bounds a Retry-After read by Failure: it is only shown, never
// waited for, and the limits of the platform count by the hour.
const MaxDelay = 24 * time.Hour

// Delay reads a Retry-After in seconds (the only form the platform sends),
// bounded by MaxDelay; 0 when absent or unreadable.
func Delay(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n < 1 {
		return 0
	}
	return min(time.Duration(n)*time.Second, MaxDelay)
}

func (e *APIError) Error() string { return codeAPIPrefix + e.Code }

// Get sends an authenticated GET to the API. The client of the account
// package follows no redirect, so the token is never sent to another host;
// a 401 INVALID_TOKEN is answered by one refresh and one replay there.
func Get(ctx context.Context, c *account.Client, path string, hc *http.Client, header http.Header) (*http.Response, error) {
	return c.DoAuthorized(ctx, hc, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API+path, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	})
}

// Failure turns an answer that is not a 200 into an *APIError when it
// carries a code of the list, else into an *account.StatusError.
func Failure(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	var body struct {
		Code    string  `json:"code"`
		State   *string `json:"state"`
		URL     any     `json:"url"`
		Minimum any     `json:"minimum"`
	}
	if json.Unmarshal(raw, &body) == nil && reCode.MatchString(body.Code) {
		e := &APIError{Status: resp.StatusCode, Code: body.Code, RetryAfter: Delay(resp.Header.Get("Retry-After"))}
		if u, ok := body.URL.(string); ok && rePageURL.MatchString(u) {
			e.URL = u
		}
		if m, ok := body.Minimum.(string); ok && body.Code == codeCLITooOld && reMinimum.MatchString(m) {
			e.Minimum = m
		}
		if body.Code == codeEntitlement && body.State != nil && reState.MatchString(*body.State) {
			e.State = *body.State
		}
		return e
	}
	return &account.StatusError{Status: resp.StatusCode, RetryAfter: Delay(resp.Header.Get("Retry-After"))}
}
