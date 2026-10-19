// Package xdg resolves the base directories of the XDG Base Directory
// specification, in one place: the variable when it holds an absolute path,
// else the default under the home directory (itself absolute). A relative
// value is ignored, as the specification says. Nothing is created here.
package xdg

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNoHome: the variable is unusable and the home directory is unknown or
// not absolute.
var ErrNoHome = errors.New(codeNoHome)

const codeNoHome = "XDG_NO_HOME"

// ConfigHome is $XDG_CONFIG_HOME, or ~/.config.
func ConfigHome() (string, error) { return base("XDG_CONFIG_HOME", ".config") }

// CacheHome is $XDG_CACHE_HOME, or ~/.cache.
func CacheHome() (string, error) { return base("XDG_CACHE_HOME", ".cache") }

// StateHome is $XDG_STATE_HOME, or ~/.local/state.
func StateHome() (string, error) { return base("XDG_STATE_HOME", filepath.Join(".local", "state")) }

func base(env, fallback string) (string, error) {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", ErrNoHome
	}
	return filepath.Join(home, fallback), nil
}
