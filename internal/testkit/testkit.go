// Package testkit gives the tests the frozen kits of testdata/ (described in
// testdata/README.md): a kit archive as the platform serves it and, for each
// state of a workspace, the result of the reference executor. The tests of
// the oracle and of the command compare the CLI with these results; nothing
// outside this repository is needed.
package testkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/giis-io/sagnac-cli/internal/kit"
)

// Kit describes testdata/<name>/kit.tar.gz (testdata/<name>/kit.json).
type Kit struct {
	Lab          string `json:"lab"`
	Version      int    `json:"version"`
	Schema       string `json:"schema"`
	SHA256       string `json:"sha256"`
	ChecksSHA256 string `json:"checks_sha256"`
	Seed         int64  `json:"seed"`
}

// State is one workspace of the kit and what the oracle must answer for it:
// the declared verdict (1 for the starting point and the wrong solutions, 0
// for the reference solution) and the full result of the reference
// executor, without the fields that change from one run to the next
// (started_at, duration_ms, cli, platform).
type State struct {
	Name             string         `json:"-"`
	Workspace        string         `json:"-"`
	DeclaredExit     int            `json:"declared_exit"`
	DeclaredFailures []string       `json:"declared_failures"`
	Result           map[string]any `json:"result"`
}

// Dir is the testdata directory at the root of the repository.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// Load reads the description of a frozen kit and its states, sorted by name.
func Load(t testing.TB, name string) (Kit, []State) {
	t.Helper()
	root := filepath.Join(Dir(), name)
	var k Kit
	readJSON(t, filepath.Join(root, "kit.json"), &k)
	dirs, err := os.ReadDir(filepath.Join(root, "states"))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var states []State
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, "states", d.Name())
		s := State{Name: d.Name(), Workspace: filepath.Join(dir, "workspace")}
		readJSON(t, filepath.Join(dir, "expected.json"), &s)
		sort.Strings(s.DeclaredFailures)
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
	return k, states
}

// Install checks the fingerprint of kit.tar.gz against kit.json, then
// extracts it like sagnac pull does into a new temporary directory, which
// it returns: it holds <lab>/ and _cadre/.
func Install(t testing.TB, name string) string {
	t.Helper()
	k, _ := Load(t, name)
	archive, err := os.ReadFile(filepath.Join(Dir(), name, "kit.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != k.SHA256 {
		t.Fatalf("fixture %s: kit.tar.gz has SHA-256 %x, kit.json announces %s", name, sum, k.SHA256)
	}
	dir := t.TempDir()
	staged, err := kit.Extract(bytes.NewReader(archive), dir, k.Lab)
	if err != nil {
		t.Fatalf("fixture %s: extract: %v", name, err)
	}
	defer staged.Remove()
	if err := staged.Install(dir); err != nil {
		t.Fatalf("fixture %s: install: %v", name, err)
	}
	return dir
}

// CopyWorkspace copies the workspace of s into dst, created if needed. Only
// the executable bit of the files is kept, as git keeps it.
func CopyWorkspace(t testing.TB, s State, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Workspace); os.IsNotExist(err) {
		return
	}
	err := filepath.WalkDir(s.Workspace, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.Workspace, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o100 != 0 {
			mode = 0o755
		}
		return copyFile(p, target, mode)
	})
	if err != nil {
		t.Fatalf("fixture %s: %v", s.Name, err)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func readJSON(t testing.TB, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("fixture %s: %v", path, err)
	}
}
