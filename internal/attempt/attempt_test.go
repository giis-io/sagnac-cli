package attempt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
)

func client(t *testing.T, h http.HandlerFunc) *account.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	store := &account.MemoryStore{}
	if err := store.Save(&account.Tokens{API: srv.URL, Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c, err := account.NewClient(srv.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A session is used only if its identifier is a UUID and its seed is in
// [0, 2^31): the seed goes into the environment of the checks.
func TestNewSessionChecksTheAnswer(t *testing.T) {
	cases := map[string]bool{
		`{"session": "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c", "seed": 0}`:          true,
		`{"session": "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c", "seed": 2147483647}`: true,
		`{"session": "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c", "seed": 2147483648}`: false,
		`{"session": "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c", "seed": -1}`:         false,
		`{"session": "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c"}`:                     false,
		`{"session": "../x", "seed": 1}`:                                          false,
		`{"session": "5F0C2B7A-3D4E-4F6A-8B9C-0D1E2F3A4B5C\n", "seed": 1}`:        false,
		`{"session": "5f0c2b7a-3d4e-4f6a-8b9c-0d1e2f3a4b5c", "seed": "1"}`:        false,
		`not json`: false,
	}
	for body, ok := range cases {
		c := client(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, body)
		})
		s, err := NewSession(context.Background(), c, "hello", 1, "0")
		if ok != (err == nil) || (ok && s == nil) {
			t.Errorf("%s: %+v, %v", body, s, err)
		}
	}
}

func TestRetryable(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	err := Send(context.Background(), c, "k", []byte(`{}`))
	if !Retryable(err) {
		t.Errorf("502: %v not retryable", err)
	}
	for _, e := range []error{&api.APIError{Status: 409, Code: "INVALID_SESSION"}, &account.StatusError{Status: 400},
		account.ErrSessionEnded, errors.New("x"), context.Canceled,
		&url.Error{Op: "Post", URL: "https://x", Err: context.Canceled},
		&url.Error{Op: "Post", URL: "https://x", Err: x509.UnknownAuthorityError{}},
		&url.Error{Op: "Post", URL: "https://x", Err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "x"}},
		&url.Error{Op: "Post", URL: "https://x", Err: &tls.CertificateVerificationError{Err: errors.New("x")}}} {
		if Retryable(e) {
			t.Errorf("%v retryable", e)
		}
	}
	for _, e := range []error{&api.APIError{Status: 500, Code: "INTERNAL_ERROR"}, context.DeadlineExceeded} {
		if !Retryable(e) {
			t.Errorf("%v not retryable", e)
		}
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	store := &account.MemoryStore{}
	_ = store.Save(&account.Tokens{API: url, Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour)})
	down, _ := account.NewClient(url, store)
	if err := Send(context.Background(), down, "k", []byte(`{}`)); !Retryable(err) {
		t.Errorf("closed server: %v not retryable", err)
	}
}

// 429 is retryable and carries the Retry-After of the answer, coded or not;
// an unreadable header gives 0 and a huge one is bounded.
func TestRateLimited(t *testing.T) {
	for _, tc := range []struct {
		body, header string
		want         time.Duration
	}{
		{`{"code": "TOO_MANY_REQUESTS"}`, "3600", time.Hour},
		{``, "45", 45 * time.Second},
		{`{"code": "TOO_MANY_REQUESTS"}`, "Wed, 21 Oct 2026 07:28:00 GMT", 0},
		{`{"code": "TOO_MANY_REQUESTS"}`, "999999999", api.MaxDelay},
	} {
		c := client(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", tc.header)
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, tc.body)
		})
		err := Send(context.Background(), c, "k", []byte(`{}`))
		delay, ok := RateLimited(err)
		if !ok || delay != tc.want || !Retryable(err) {
			t.Errorf("%q %q: RateLimited = %s %v, Retryable %v (%v)", tc.body, tc.header, delay, ok, Retryable(err), err)
		}
	}
	if _, ok := RateLimited(&api.APIError{Status: 503, Code: "SERVICE_OVERLOADED"}); ok {
		t.Error("503 seen as 429")
	}
}
