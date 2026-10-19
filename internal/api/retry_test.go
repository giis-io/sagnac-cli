package api

import (
	"testing"
	"time"
)

func TestRetryAfterFallbackAndBound(t *testing.T) {
	for _, c := range []struct {
		header string
		want   time.Duration
	}{
		{"", 5 * time.Second},
		{"abc", 5 * time.Second},
		{"0", 5 * time.Second},
		{"-3", 5 * time.Second},
		{"30", 30 * time.Second},
		{" 7 ", 7 * time.Second},
		{"9999", 120 * time.Second},
	} {
		if got := retryAfter(c.header); got != c.want {
			t.Errorf("retryAfter(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}
