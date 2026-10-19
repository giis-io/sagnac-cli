// Package attempt holds the two calls of "sagnac check" to the platform:
// POST /v1/sessions, which gives the seed of a connected run, and
// POST /v1/attempts, which records the result as a declared attempt
// (docs/contrat.md, section 4). The token goes in the Authorization
// header only, through the client of the account package (no redirect, one
// refresh and one replay on 401 INVALID_TOKEN).
package attempt

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
)

const (
	maxBody = 64 << 10
	maxSeed = 1 << 31 // the server draws in [0, 2^31), like the local seed
)

var reSession = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Session is the answer of POST /v1/sessions, checked.
type Session struct {
	ID   string
	Seed int64
}

// NewSession asks for a seed for this lab, version and kit digest. An answer
// that is not a 201 comes back as *api.APIError (a code of the list) or
// *account.StatusError.
func NewSession(ctx context.Context, c *account.Client, lab string, version int64, checksSHA256 string) (*Session, error) {
	body, err := json.Marshal(map[string]any{"lab": lab, "version": version, "checks_sha256": checksSHA256})
	if err != nil {
		return nil, err
	}
	resp, err := post(ctx, c, "/v1/sessions", body, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, api.Failure(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var s struct {
		Session string `json:"session"`
		Seed    *int64 `json:"seed"`
	}
	if json.Unmarshal(raw, &s) != nil || !reSession.MatchString(s.Session) || s.Seed == nil ||
		*s.Seed < 0 || *s.Seed >= maxSeed {
		return nil, api.ErrBadResponse
	}
	return &Session{ID: s.Session, Seed: *s.Seed}, nil
}

// Send posts result (JSON of schema sagnac-result/2) under the
// idempotency key. 200 (already received) and 201 are a success; anything
// else comes back as *api.APIError or *account.StatusError.
func Send(ctx context.Context, c *account.Client, key string, result []byte) error {
	resp, err := post(ctx, c, "/v1/attempts", result, http.Header{"Idempotency-Key": {key}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return nil
	}
	return api.Failure(resp)
}

func post(ctx context.Context, c *account.Client, path string, body []byte, header http.Header) (*http.Response, error) {
	return c.DoAuthorized(ctx, nil, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	})
}

// RateLimited reports whether the server answered 429 (too many requests of
// this account), and the delay of its Retry-After (0 when absent).
func RateLimited(err error) (time.Duration, bool) {
	var apiErr *api.APIError
	var status *account.StatusError
	switch {
	case errors.As(err, &apiErr):
		return apiErr.RetryAfter, apiErr.Status == http.StatusTooManyRequests
	case errors.As(err, &status):
		return status.RetryAfter, status.Status == http.StatusTooManyRequests
	}
	return 0, false
}

// Retryable reports whether err is worth sending again later: the network
// failed, or the server answered 5xx or 429. Any other 4xx, an interruption
// or a certificate error never is (docs/contrat.md, section 4 bis).
func Retryable(err error) bool {
	var netErr net.Error
	var status *account.StatusError
	var apiErr *api.APIError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	switch {
	// Ctrl-C, or a certificate that is not trusted: sending again later
	// changes nothing (and a certificate error may be an interception).
	case errors.Is(err, context.Canceled), errors.As(err, &unknownAuthority), errors.As(err, &hostname),
		errors.As(err, &invalid), errors.As(err, &verification):
		return false
	case errors.As(err, &apiErr):
		return apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests
	case errors.As(err, &status):
		return status.Status >= 500 || status.Status == http.StatusTooManyRequests
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return true
	}
	return false
}
