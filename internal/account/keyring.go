package account

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrKeyringFailed: the keyring tool refused or failed (locked keychain,
// no Secret Service daemon). The command can offer --session.
var ErrKeyringFailed = errors.New(codeKeyringFailed)

// keyringService names the item in the keyring; the account is the API
// address, so tokens of a test server never mix with those of sagnac.fr.
const keyringService = "sagnac-cli"

// securityTool is the macOS keychain tool, by absolute path so that PATH
// cannot substitute it. A variable for the tests.
var securityTool = "/usr/bin/security"

// The secret goes through the tools as the hexadecimal form of the JSON of
// Tokens: nothing for the command parser of "security -i" to interpret.
func encodeSecret(t *Tokens) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func decodeSecret(out []byte) (*Tokens, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, ErrKeyringFailed
	}
	var t Tokens
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, ErrKeyringFailed
	}
	return &t, nil
}

func run(tool string, stdin string, args ...string) (stdout, stderr []byte, code int, err error) {
	cmd := exec.Command(tool, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errOut.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errOut.Bytes(), 0, err
}

// safeWord: what may be put between double quotes on a "security -i" line.
func safeWord(s string) bool {
	return s != "" && !strings.ContainsAny(s, "\"\\'") && !strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	})
}

// KeychainStore keeps the tokens in the login keychain of macOS through
// /usr/bin/security, without a third-party library or cgo. The secret is
// written by "security -i", which reads its command on stdin: it never
// appears in the arguments of a process (ps). Reading prints it on the
// standard output of security, a pipe to this process.
type KeychainStore struct {
	Tool    string
	Account string
}

// maxSecurityLine is the longest line "security -i" reads (4096 bytes, less a margin).
const maxSecurityLine = 4000

const keychainNotFound = 44 // errSecItemNotFound as exit code of security

func keychainAvailable() bool {
	if _, err := os.Stat(securityTool); err != nil {
		return false
	}
	_, _, code, err := run(securityTool, "", "default-keychain")
	return err == nil && code == 0
}

func (k *KeychainStore) Load() (*Tokens, error) {
	out, _, code, err := run(k.Tool, "", "find-generic-password", "-s", keyringService, "-a", k.Account, "-w")
	switch {
	case err != nil:
		return nil, err
	case code == keychainNotFound:
		return nil, nil
	case code != 0:
		return nil, ErrKeyringFailed
	}
	return decodeSecret(out)
}

func (k *KeychainStore) Save(t *Tokens) error {
	if !safeWord(k.Account) {
		return ErrKeyringFailed
	}
	secret, err := encodeSecret(t)
	if err != nil {
		return err
	}
	line := `add-generic-password -U -s "` + keyringService + `" -a "` + k.Account + `" -w ` + secret + "\n"
	// "security -i" cuts a longer line without saying so.
	if len(line) > maxSecurityLine {
		return ErrKeyringFailed
	}
	_, errOut, code, err := run(k.Tool, line, "-i")
	if err != nil {
		return err
	}
	// "security -i" exits 0 even when a command fails: read back to be sure.
	if code != 0 || len(bytes.TrimSpace(errOut)) != 0 {
		return ErrKeyringFailed
	}
	back, err := k.Load()
	if err != nil || back == nil || !sameTokens(back, t) {
		return ErrKeyringFailed
	}
	return nil
}

func (k *KeychainStore) Delete() error {
	_, _, code, err := run(k.Tool, "", "delete-generic-password", "-s", keyringService, "-a", k.Account)
	if err != nil {
		return err
	}
	if code != 0 && code != keychainNotFound {
		return ErrKeyringFailed
	}
	return nil
}

// SecretServiceStore keeps the tokens in the Secret Service of the session
// (GNOME Keyring, KeePassXC…) through secret-tool (libsecret). "store" reads
// the secret on stdin; it never appears in the arguments of a process.
type SecretServiceStore struct {
	Tool    string
	Account string
}

// secretServiceAvailable: secret-tool on PATH and a D-Bus session (absent
// under WSL2 and over a bare SSH session, the usual cases without keyring).
func secretServiceAvailable() (string, bool) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return "", false
	}
	tool, err := exec.LookPath("secret-tool")
	return tool, err == nil
}

func (s *SecretServiceStore) attrs() []string {
	return []string{"service", keyringService, "account", s.Account}
}

func (s *SecretServiceStore) Load() (*Tokens, error) {
	out, errOut, code, err := run(s.Tool, "", append([]string{"lookup"}, s.attrs()...)...)
	switch {
	case err != nil:
		return nil, err
	case code == 1 && len(out) == 0 && len(bytes.TrimSpace(errOut)) == 0:
		return nil, nil // no such secret
	case code != 0:
		return nil, ErrKeyringFailed
	}
	return decodeSecret(out)
}

func (s *SecretServiceStore) Save(t *Tokens) error {
	secret, err := encodeSecret(t)
	if err != nil {
		return err
	}
	args := append([]string{"store", "--label=" + keyringService}, s.attrs()...)
	_, _, code, err := run(s.Tool, secret, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrKeyringFailed
	}
	return nil
}

func (s *SecretServiceStore) Delete() error {
	_, errOut, code, err := run(s.Tool, "", append([]string{"clear"}, s.attrs()...)...)
	if err != nil {
		return err
	}
	if code != 0 && !(code == 1 && len(bytes.TrimSpace(errOut)) == 0) {
		return ErrKeyringFailed
	}
	return nil
}

func sameTokens(a, b *Tokens) bool {
	return a.API == b.API && a.Access == b.Access && a.Refresh == b.Refresh &&
		a.Scope == b.Scope && a.ExpiresAt.Equal(b.ExpiresAt) && a.Login == b.Login
}
