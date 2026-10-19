package oracle

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Schema is the identifier of the result format (docs/contrat.md, section 4).
const Schema = "sagnac-result/2"

// CLIName fills the cli field of the result; the command sets it to its
// own name and version.
var CLIName = "sagnac/dev"

// Exit codes of a check (contract v2, section 3).
const (
	ExitPass     = 0
	ExitFail     = 1
	ExitPlatform = 2
	ExitOracle   = 3
	ExitUsage    = 64
)

// Statuses and origins written in the result.
const (
	statusPass  = "pass"
	statusFail  = "fail"
	statusError = "error"
	statusSkip  = "skip"

	originStudent  = "student"
	originPlatform = "platform"
	originOracle   = "oracle"
)

// Result is the output of one run, serialised exactly as schema
// sagnac-result/2.
type Result struct {
	Schema       string        `json:"schema"`
	CLI          string        `json:"cli"`
	Lab          LabRef        `json:"lab"`
	ChecksSHA256 string        `json:"checks_sha256"`
	Session      string        `json:"session"`
	Seed         int64         `json:"seed"`
	Start        string        `json:"started_at"`
	DurationMS   int64         `json:"duration_ms"`
	Status       string        `json:"status"`
	Exit         int           `json:"exit_code"`
	Checks       []CheckResult `json:"checks"`
	Platform     Platform      `json:"platform"`
}

// LabRef identifies the lab of a result.
type LabRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// CheckResult is the outcome of one check. Code and Origin are null for pass
// and skip; Observed and Expected are typed values (bool, int64, string) or null.
type CheckResult struct {
	ID         string  `json:"id"`
	Step       int64   `json:"step"`
	Status     string  `json:"status"`
	Code       *string `json:"code"`
	Origin     *string `json:"origin"`
	Observed   any     `json:"observed"`
	Expected   any     `json:"expected"`
	BlockedBy  *string `json:"blocked_by"`
	DurationMS int64   `json:"duration_ms"`
}

// Platform describes the machine of the run, without any local name.
type Platform struct {
	System          string  `json:"os"`
	Architecture    string  `json:"architecture"`
	ContainerEngine *string `json:"container_engine"`
	EngineVersion   *string `json:"engine_version"`
}

// ExitCode is the exit code of sagnac check for this result.
func (r *Result) ExitCode() int { return r.Exit }

// currentPlatform describes the machine: its real architecture, not the one
// of this binary (an amd64 binary under Rosetta still reports arm64).
func currentPlatform() Platform {
	arch := machineArch()
	if arch == "" {
		arch = runtime.GOARCH
	}
	return Platform{System: runtime.GOOS, Architecture: arch}
}

// normalizeArch names an architecture like the reference executor does.
func normalizeArch(machine string) string {
	m := strings.ToLower(machine)
	switch m {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return m
}

// ChecksDigest is checks_sha256: the digest of meta.yaml and checks/, without
// checks/mutants/ and checks/RESULTAT.md, executable bit included. Entries
// come in path order, component by component (depth-first walk, names sorted).
func ChecksDigest(dir string) (string, error) {
	dir = resolve(dir)
	h := sha256.New()
	add := func(rel, p string) error {
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(rel + "\x00"))
		if executable(p) {
			h.Write([]byte("x"))
		} else {
			h.Write([]byte("-"))
		}
		sum := sha256.Sum256(content)
		h.Write(sum[:])
		return nil
	}
	if err := add("meta.yaml", filepath.Join(dir, "meta.yaml")); err != nil {
		return "", err
	}
	root := filepath.Join(dir, "checks")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if p == root {
			return nil
		}
		rel := filepath.ToSlash(p[len(root)+1:])
		if rel == "mutants" || rel == "RESULTAT.md" || slices.Contains(strings.Split(rel, "/"), "__pycache__") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" || !isRegular(p) {
			return nil
		}
		return add("checks/"+rel, p)
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
