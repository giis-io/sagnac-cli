package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func answer(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestFailureReadsRetryAt(t *testing.T) {
	for _, c := range []struct {
		body string
		want time.Time
	}{
		{`{"code": "TOO_MANY_REQUESTS", "retry_at": "2026-10-24T13:05:00Z"}`, time.Date(2026, 10, 24, 13, 5, 0, 0, time.UTC)},
		{`{"code": "TOO_MANY_REQUESTS"}`, time.Time{}},
		{`{"code": "TOO_MANY_REQUESTS", "retry_at": "2026-10-24T13:05:00+02:00"}`, time.Time{}},
		{`{"code": "TOO_MANY_REQUESTS", "retry_at": "tomorrow"}`, time.Time{}},
		{`{"code": "TOO_MANY_REQUESTS", "retry_at": 1761310000}`, time.Time{}},
	} {
		err := Failure(answer(429, c.body, http.Header{"Retry-After": {"90"}}))
		var e *APIError
		if !errors.As(err, &e) {
			t.Fatalf("%s: %v", c.body, err)
		}
		if !e.RetryAt.Equal(c.want) || e.RetryAfter != 90*time.Second {
			t.Errorf("%s: retry_at %v, Retry-After %v", c.body, e.RetryAt, e.RetryAfter)
		}
	}
}

func TestFailureReadsKitLimit(t *testing.T) {
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"code": "KIT_DOWNLOAD_LIMIT", "limit": 30}`, 30},
		{`{"code": "KIT_DOWNLOAD_LIMIT"}`, 0},
		{`{"code": "KIT_DOWNLOAD_LIMIT", "limit": "30"}`, 0},
		{`{"code": "KIT_DOWNLOAD_LIMIT", "limit": 2.5}`, 0},
		{`{"code": "KIT_DOWNLOAD_LIMIT", "limit": 0}`, 0},
		{`{"code": "KIT_DOWNLOAD_LIMIT", "limit": 1e12}`, 0},
		{`{"code": "TOO_MANY_REQUESTS", "limit": 30}`, 0},
	} {
		var e *APIError
		if !errors.As(Failure(answer(429, c.body, nil)), &e) || e.Limit != c.want {
			t.Errorf("%s: %+v", c.body, e)
		}
	}
}
