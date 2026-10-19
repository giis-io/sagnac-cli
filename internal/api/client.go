// Package api holds the calls of the CLI to the platform that are not token
// requests: the device code (RFC 8628 § 3.1) and the revocation (RFC 7009).
// Everything a server answers is checked before it is used or shown: the
// commands print catalogue messages, never text that came from the network.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
)

const (
	codeForeignAddress = "API_FOREIGN_ADDRESS"
	clientID           = "sagnac-cli"
	maxBody            = 64 << 10
	// The server gives 600 s and 5 s.
	maxExpiresIn       = 15 * 60
	maxInterval        = 60
	defaultInterval    = 5
	defaultRetryAfter  = 5 * time.Second
	userCodeLetters    = "BCDFGHJKLMNPQRSTVWXZ"
	verificationSuffix = "/appareil/"
)

var (
	reDeviceCode = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
	reUserCode   = regexp.MustCompile(`^[` + userCodeLetters + `]{4}-[` + userCodeLetters + `]{4}$`)
	rePrintable  = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
)

// DeviceCode is the answer of /v1/device/code, checked.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       time.Duration
	Interval        time.Duration
}

// RetryError: the platform is busy (429 or 503); the request can be sent
// again after After (docs/api/erreurs.md: TOO_MANY_REQUESTS, SERVICE_OVERLOADED).
type RetryError struct {
	Status int
	After  time.Duration
}

func (e *RetryError) Error() string { return "API_RETRY_" + strconv.Itoa(e.Status) }

type badResponse struct{}

func (badResponse) Error() string { return "API_BAD_RESPONSE" }

// ErrForeignAddress: the verification address is not the device page of the
// configured server (other origin, other path, query, escape, or it carries the
// code). It is never shown.
var ErrForeignAddress = errors.New(codeForeignAddress)

// ErrBadResponse is returned for a success answer (200, 201) that does not
// follow the contract; the only sentinel of the CLI for it.
var ErrBadResponse error = badResponse{}

func postForm(ctx context.Context, c *account.Client, path string, form url.Values) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	c.SetUserAgent(req)
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp, raw, err
}

// RequestDeviceCode asks for a device code for this machine. The answer must
// carry a user code of the platform's alphabet, an address of the API's own
// origin that does not contain the code, and bounded durations.
func RequestDeviceCode(ctx context.Context, c *account.Client, deviceID, deviceName string) (*DeviceCode, error) {
	resp, raw, err := postForm(ctx, c, "/v1/device/code", url.Values{
		"client_id": {clientID}, "device_id": {deviceID}, "device_name": {deviceName},
	})
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return nil, &RetryError{Status: resp.StatusCode, After: retryAfter(resp.Header.Get("Retry-After"))}
	default:
		return nil, &account.StatusError{Status: resp.StatusCode}
	}
	var body struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, ErrBadResponse
	}
	if body.Interval == 0 {
		body.Interval = defaultInterval // RFC 8628 § 3.2: 5 s when absent
	}
	if !sameOriginAddress(c.API, body.VerificationURI, body.UserCode) {
		return nil, ErrForeignAddress
	}
	if !reDeviceCode.MatchString(body.DeviceCode) || !reUserCode.MatchString(body.UserCode) ||
		body.ExpiresIn < 1 || body.ExpiresIn > maxExpiresIn || body.Interval < 1 || body.Interval > maxInterval {
		return nil, ErrBadResponse
	}
	return &DeviceCode{
		DeviceCode:      body.DeviceCode,
		UserCode:        body.UserCode,
		VerificationURI: body.VerificationURI,
		ExpiresIn:       time.Duration(body.ExpiresIn) * time.Second,
		Interval:        time.Duration(body.Interval) * time.Second,
	}, nil
}

// sameOriginAddress: uri is printable ASCII, the device page of the API, with
// no user information, query, fragment or escape, and without the user code
// (the code is typed by the person, never carried by an address).
func sameOriginAddress(api, uri, userCode string) bool {
	if !rePrintable.MatchString(uri) || strings.Contains(uri, "%") {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		!strings.EqualFold(u.Scheme+"://"+u.Host, api) || u.Path != verificationSuffix {
		return false
	}
	flat := strings.ToUpper(strings.ReplaceAll(uri, "-", ""))
	return !strings.Contains(flat, strings.ReplaceAll(userCode, "-", ""))
}

// retryAfter: the Retry-After of the answer, bounded by account.MaxRetryAfter
// (120 s); defaultRetryAfter (5 s) when the header is absent or unreadable.
func retryAfter(h string) time.Duration {
	if d := account.ParseRetryAfter(h); d > 0 {
		return d
	}
	return defaultRetryAfter
}

// Revoke asks the platform to close the family of the refresh token
// (RFC 7009). Anything but a 200 is an error: the caller says so.
func Revoke(ctx context.Context, c *account.Client, refreshToken string) error {
	resp, _, err := postForm(ctx, c, "/v1/revoke", url.Values{"client_id": {clientID}, "token": {refreshToken}})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &account.StatusError{Status: resp.StatusCode}
	}
	return nil
}
