package xdg

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestHomesFollowTheVariableThenFallBackToHome(t *testing.T) {
	for _, c := range []struct {
		name     string
		env      string
		fn       func() (string, error)
		fallback []string
	}{
		{"config", "XDG_CONFIG_HOME", ConfigHome, []string{".config"}},
		{"cache", "XDG_CACHE_HOME", CacheHome, []string{".cache"}},
		{"state", "XDG_STATE_HOME", StateHome, []string{".local", "state"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			want := filepath.Join(append([]string{home}, c.fallback...)...)
			for _, v := range []string{"", "relative/dir", "."} {
				t.Setenv(c.env, v)
				if got, err := c.fn(); err != nil || got != want {
					t.Errorf("%s=%q: %q, %v; want %q", c.env, v, got, err, want)
				}
			}
			abs := t.TempDir()
			t.Setenv(c.env, abs)
			if got, err := c.fn(); err != nil || got != abs {
				t.Errorf("%s=%q: %q, %v", c.env, abs, got, err)
			}
		})
	}
}

func TestNoUsableHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "relative")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := CacheHome(); !errors.Is(err, ErrNoHome) {
		t.Errorf("no home: %v", err)
	}
	t.Setenv("HOME", "relative/home")
	if _, err := CacheHome(); !errors.Is(err, ErrNoHome) {
		t.Errorf("relative home: %v", err)
	}
}
