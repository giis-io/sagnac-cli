// Package account holds what the CLI keeps about the person and the machine:
// the server addresses, the stable device identifier and the tokens of the
// device flow (RFC 8628), refreshed under a
// file lock so that two CLIs never spend the same refresh token.
package account

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/giis-io/sagnac-cli/internal/xdg"
)

// Codes of the errors of this package. They are never shown as such: the
// command maps them to a catalogue message.
const (
	codeInsecureURL   = "ACCOUNT_INSECURE_URL"
	codeNoConfigDir   = "ACCOUNT_NO_CONFIG_DIR"
	codeNotLoggedIn   = "ACCOUNT_NOT_LOGGED_IN"
	codeSessionEnded  = "ACCOUNT_SESSION_ENDED"
	codeBadResponse   = "ACCOUNT_BAD_TOKEN_RESPONSE"
	codeKeyringFailed = "ACCOUNT_KEYRING_FAILED"
)

var (
	// ErrInsecureURL: the address is not https, or http outside the loopback.
	ErrInsecureURL = errors.New(codeInsecureURL)
	// ErrNoConfigDir: neither XDG_CONFIG_HOME nor HOME gives a directory.
	ErrNoConfigDir = errors.New(codeNoConfigDir)
)

const (
	envAPI            = "SAGNAC_API"
	envVersionURL     = "SAGNAC_VERSION_URL"
	defaultAPI        = "https://sagnac.fr"
	defaultVersionURL = "https://sagnac.fr/cli/version.json"
)

// URLAPI returns the base address of the API: SAGNAC_API, or https://sagnac.fr.
// It is reduced to scheme://host[:port]; a path, a query, a fragment or user
// information is refused, and so is http:// outside 127.0.0.1 and localhost.
func URLAPI() (string, error) {
	u, err := checkURL(envOr(envAPI, defaultAPI))
	if err != nil {
		return "", err
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.ForceQuery {
		return "", ErrInsecureURL
	}
	return u.Scheme + "://" + u.Host, nil
}

// URLVersion returns the address of the static version file:
// SAGNAC_VERSION_URL, or https://sagnac.fr/cli/version.json, with the same
// http:// rule as URLAPI.
func URLVersion() (string, error) {
	u, err := checkURL(envOr(envVersionURL, defaultVersionURL))
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInsecureURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "https":
	case "http":
		if h := strings.ToLower(u.Hostname()); h != "127.0.0.1" && h != "localhost" {
			return nil, ErrInsecureURL
		}
	default:
		return nil, ErrInsecureURL
	}
	return u, nil
}

// ConfigDir returns $XDG_CONFIG_HOME/sagnac, or ~/.config/sagnac when the
// variable is empty or relative (XDG Base Directory). It does not create it.
func ConfigDir() (string, error) {
	base, err := xdg.ConfigHome()
	if err != nil {
		return "", ErrNoConfigDir
	}
	return filepath.Join(base, "sagnac"), nil
}

// ensureConfigDir creates the configuration directory with mode 0700.
func ensureConfigDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
