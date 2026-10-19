package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
)

const testAccess = "sgn_a_SECRETACCESS"

type server struct {
	mu       sync.Mutex
	srv      *httptest.Server
	client   *account.Client
	requests []*http.Request
	handler  http.HandlerFunc
}

func newServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()
	s := &server{handler: h}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.mu.Unlock()
		s.handler(w, r)
	}))
	t.Cleanup(s.srv.Close)
	store := &account.MemoryStore{}
	if err := store.Save(&account.Tokens{API: s.srv.URL, Access: testAccess, Refresh: "sgn_r_x",
		ExpiresAt: time.Now().Add(time.Hour), Scope: "kits:read"}); err != nil {
		t.Fatal(err)
	}
	c, err := account.NewClient(s.srv.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	s.client = c
	return s
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestCurrentReadsVersion(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"lab": "hello", "version": 3, "kit_sha256": %q}`, digest)
	})
	info, err := Current(context.Background(), s.client, "hello")
	if err != nil || info.Version != 3 || info.SHA256 != digest || info.Lab != "hello" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	r := s.requests[0]
	if r.Method != http.MethodGet || r.URL.Path != "/v1/kits/hello" || r.URL.RawQuery != "" ||
		r.Header.Get("Authorization") != "Bearer "+testAccess {
		t.Errorf("request %s %s, Authorization %q", r.Method, r.URL, r.Header.Get("Authorization"))
	}
}

func TestCurrentRefusesBadAnswers(t *testing.T) {
	for _, body := range []string{
		`{"lab": "other", "version": 3, "kit_sha256": "` + strings.Repeat("a", 64) + `"}`,
		`{"lab": "hello", "version": 0, "kit_sha256": "` + strings.Repeat("a", 64) + `"}`,
		`{"lab": "hello", "version": 3, "kit_sha256": "` + strings.Repeat("A", 64) + `"}`,
		`{"lab": "hello", "version": 3, "kit_sha256": "abc"}`,
		`not json`,
	} {
		s := newServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := Current(context.Background(), s.client, "hello"); !errors.Is(err, api.ErrBadResponse) {
			t.Errorf("%s: err = %v", body, err)
		}
	}
}

func TestErrorCodesComeBackAsAPIError(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
		state  string
	}{
		{403, `{"code": "ENTITLEMENT_INACTIVE", "state": "unpaid"}`, "ENTITLEMENT_INACTIVE", "unpaid"},
		{403, `{"code": "ENTITLEMENT_INACTIVE", "state": null}`, "ENTITLEMENT_INACTIVE", ""},
		{403, `{"code": "ENTITLEMENT_INACTIVE", "state": "<script>"}`, "ENTITLEMENT_INACTIVE", ""},
		{404, `{"code": "UNKNOWN_LAB"}`, "UNKNOWN_LAB", ""},
		{403, `{"code": "INSUFFICIENT_SCOPE"}`, "INSUFFICIENT_SCOPE", ""},
	}
	for _, c := range cases {
		s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			fmt.Fprint(w, c.body)
		})
		_, err := Current(context.Background(), s.client, "hello")
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != c.code || apiErr.State != c.state || apiErr.Status != c.status {
			t.Errorf("%s: err = %#v", c.body, err)
		}
	}
	// A body without a code of the list is a status error, its text unused.
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		fmt.Fprint(w, `<html>Bad gateway</html>`)
	})
	_, err := Current(context.Background(), s.client, "hello")
	var status *account.StatusError
	if !errors.As(err, &status) || status.Status != 502 {
		t.Errorf("502: err = %v", err)
	}
}

func TestDownloadVerifiesBothDigests(t *testing.T) {
	archive := []byte("kit bytes")
	good := sum(archive)
	cases := []struct {
		name     string
		header   string
		expected string
		ok       bool
	}{
		{"both match", good, good, true},
		{"header differs from content", strings.Repeat("0", 64), "", false},
		{"current version differs", good, strings.Repeat("1", 64), false},
		{"header missing", "", good, false},
	}
	for _, c := range cases {
		s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/gzip")
			if c.header != "" {
				w.Header().Set("X-Sagnac-Kit-Sha256", c.header)
			}
			w.Write(archive)
		})
		work := t.TempDir()
		f, err := Download(context.Background(), s.client, "hello", 3, work, c.expected)
		if c.ok {
			// The file comes back open and rewound, ready to be read.
			var got []byte
			if err == nil {
				got, _ = io.ReadAll(f)
				Discard(f)
			}
			if err != nil || string(got) != string(archive) {
				t.Errorf("%s: %v, %q", c.name, err, got)
			}
			if r := s.requests[0]; r.URL.Path != "/v1/kits/hello/3" || r.Header.Get("Authorization") != "Bearer "+testAccess {
				t.Errorf("request %s, Authorization %q", r.URL, r.Header.Get("Authorization"))
			}
		} else if !errors.Is(err, ErrChecksum) || f != nil {
			t.Errorf("%s: file %v, err = %v", c.name, f, err)
		}
		if entries, _ := os.ReadDir(work); len(entries) != 0 {
			t.Errorf("%s: left %v", c.name, entries)
		}
	}
}

func TestDownloadSizeBounded(t *testing.T) {
	old := MaxArchive
	t.Cleanup(func() { MaxArchive = old })
	MaxArchive = 1024
	archive := make([]byte, 4096)
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Sagnac-Kit-Sha256", sum(archive))
		w.Write(archive)
	})
	work := t.TempDir()
	_, err := Download(context.Background(), s.client, "hello", 3, work, sum(archive))
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) || unsafe.Reason != reasonTooLarge {
		t.Errorf("err = %v", err)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
}

// The token never follows a redirect and never goes into an address.
func TestTokenNotSentAfterRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed to %s, Authorization %q", r.URL, r.Header.Get("Authorization"))
	}))
	defer other.Close()
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	if _, err := Current(context.Background(), s.client, "hello"); err == nil {
		t.Error("a redirect was accepted")
	}
	if _, err := Download(context.Background(), s.client, "hello", 3, t.TempDir(), ""); err == nil {
		t.Error("a redirect was accepted")
	}
	for _, r := range s.requests {
		if strings.Contains(r.URL.String(), testAccess) {
			t.Errorf("token in the address: %s", r.URL)
		}
	}
}

func TestInvalidLabNeverSent(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := Current(context.Background(), s.client, "../v1/token"); !errors.Is(err, ErrInvalidLab) {
		t.Errorf("Current: %v", err)
	}
	if _, err := Download(context.Background(), s.client, "a/b", 1, t.TempDir(), ""); !errors.Is(err, ErrInvalidLab) {
		t.Errorf("Download: %v", err)
	}
	if len(s.requests) != 0 {
		t.Errorf("%d requests sent", len(s.requests))
	}
}

// A 401 INVALID_TOKEN is refreshed once and replayed once; the second one
// comes back as the error of the list. Download and Current share this.
func TestInvalidTokenRefreshedThenReported(t *testing.T) {
	var kits, refreshes int
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/token" {
			refreshes++
			fmt.Fprint(w, `{"access_token": "sgn_a_new", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "sgn_r_new", "scope": "kits:read"}`)
			return
		}
		kits++
		w.WriteHeader(401)
		fmt.Fprint(w, `{"code": "INVALID_TOKEN"}`)
	})
	_, err := Current(context.Background(), s.client, "hello")
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_TOKEN" || kits != 2 || refreshes != 1 {
		t.Errorf("err = %v, kit requests %d, refreshes %d", err, kits, refreshes)
	}
}

func TestAPIErrorKeepsOnlyAPrintableURL(t *testing.T) {
	for body, want := range map[string]string{
		`{"code": "OFFLINE_INSTRUCTIONS_NOT_INCLUDED", "url": "https://x.test/labs/hello/enonce?version=3"}`: "https://x.test/labs/hello/enonce?version=3",
		`{"code": "OFFLINE_INSTRUCTIONS_NOT_INCLUDED", "url": "https://x.test/\u001b[31m"}`:                  "",
		`{"code": "OFFLINE_INSTRUCTIONS_NOT_INCLUDED", "url": 5}`:                                            "",
	} {
		s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			fmt.Fprint(w, body)
		})
		_, err := Current(context.Background(), s.client, "hello")
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "OFFLINE_INSTRUCTIONS_NOT_INCLUDED" || apiErr.URL != want {
			t.Errorf("%s: err = %#v", body, err)
		}
	}
}
