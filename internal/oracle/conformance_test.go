package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/testkit"
)

// Conformance with the reference executor of the lab contract
// (docs/contrat.md). TestConformanceHello replays the frozen kit of
// testdata/hello-v3: every state of its workspace must give, field for
// field, the result the reference executor gave (testdata/README.md).
//
// TestCrossCheck runs the reference executor itself, with the wrong
// solutions of each lab, on labs that only the CI of the platform has:
// SAGNAC_XCHECK_REFERENCE is the path of the executor, SAGNAC_XCHECK_LABS the
// lab directories, separated like PATH. Without both it is skipped. A lab
// with a cluster is compared without any cluster (crossCheckCluster), unless
// SAGNAC_XCHECK_CLUSTER=1.

const conformanceSeed = 20261019

func TestConformanceHello(t *testing.T) {
	k, states := testkit.Load(t, "hello-v3")
	if k.Schema != Schema {
		t.Fatalf("fixture schema %q, CLI %q", k.Schema, Schema)
	}
	if len(states) < 5 {
		t.Fatalf("hello-v3: only %d states", len(states))
	}
	root := testkit.Install(t, "hello-v3")
	lab := mustLoad(t, filepath.Join(root, k.Lab))
	method, cut := NetworkCutAvailable()
	t.Logf("network cut: %v (%s)", cut, method)
	tmp := t.TempDir()
	for i, s := range states {
		ws := filepath.Join(tmp, "s"+strconv.Itoa(i))
		testkit.CopyWorkspace(t, s, ws)
		r, err := Verify(context.Background(), lab, ws, k.Seed, cut)
		if err != nil {
			t.Fatalf("%s: Verify: %v", s.Name, err)
		}
		goJSON, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		got := comparable(t, goJSON)
		delete(got, "platform")
		if !reflect.DeepEqual(got, s.Result) {
			want, _ := json.Marshal(s.Result)
			t.Errorf("%s: JSON differs\nreference: %s\nGo:        %s", s.Name, want, goJSON)
		}
		if r.ExitCode() != s.DeclaredExit {
			t.Errorf("%s: exit code %d, want %d", s.Name, r.ExitCode(), s.DeclaredExit)
		}
		if got := failures(r); !slices.Equal(got, s.DeclaredFailures) {
			t.Errorf("%s: failures %v, want %v", s.Name, got, s.DeclaredFailures)
		}
		if r.ChecksSHA256 != k.ChecksSHA256 {
			t.Errorf("%s: checks_sha256 %s, kit.json %s", s.Name, r.ChecksSHA256, k.ChecksSHA256)
		}
		t.Logf("%-40s exit %d  failures %v", s.Name, r.ExitCode(), failures(r))
	}
}

func TestCrossCheck(t *testing.T) {
	reference := os.Getenv("SAGNAC_XCHECK_REFERENCE")
	labs := filepath.SplitList(os.Getenv("SAGNAC_XCHECK_LABS"))
	if reference == "" || len(labs) == 0 {
		t.Skip("SAGNAC_XCHECK_REFERENCE and SAGNAC_XCHECK_LABS are not set (CI of the platform; conformance runs on testdata/)")
	}
	for _, dir := range labs {
		t.Run(filepath.Base(dir), func(t *testing.T) { crossCheck(t, reference, dir) })
	}
}

type state struct {
	name     string
	base     string // initial or solution
	mutant   string // mutant directory, empty for the base state itself
	exit     int    // expected exit code: 1 initial, 0 solution, expected_exit for a mutant
	failures []string
}

func crossCheck(t *testing.T, reference, labDir string) {
	lab := mustLoad(t, labDir)
	// SAGNAC_XCHECK_CLUSTER=1: a real cluster is there (VM of the factory);
	// make setup starts it, and the lab is checked like any other.
	if lab.Cluster != "" && os.Getenv("SAGNAC_XCHECK_CLUSTER") != "1" {
		crossCheckCluster(t, reference, labDir, lab)
		return
	}
	tmp := t.TempDir()
	for i, s := range labStates(t, labDir) {
		ws := filepath.Join(tmp, "s"+strconv.Itoa(i))
		makeTarget(t, labDir, "setup", ws)
		prepare(t, labDir, s, ws)
		r := compareWithReference(t, reference, lab, labDir, ws, s.name)
		// Red, green, red: the oracle itself, not only the agreement with the reference.
		if r.ExitCode() != s.exit {
			t.Errorf("%s: exit code %d, want %d", s.name, r.ExitCode(), s.exit)
		}
		if s.mutant != "" {
			if got := failures(r); !slices.Equal(got, s.failures) {
				t.Errorf("%s: failures %v, mutant.yaml announces %v", s.name, got, s.failures)
			}
		}
	}
}

// labStates lists the initial state, the solution and every wrong solution
// of checks/mutants/ with what its mutant.yaml announces.
func labStates(t *testing.T, labDir string) []state {
	t.Helper()
	states := []state{{name: "initial", base: "initial", exit: 1}, {name: "solution", base: "solution", exit: 0}}
	mutants, _ := filepath.Glob(filepath.Join(labDir, "checks", "mutants", "*", "mutant.yaml"))
	sort.Strings(mutants)
	for _, m := range mutants {
		raw, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := parseYAML(string(raw))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		decl := doc.(map[string]any)
		base := "solution"
		if b, ok := decl["base"].(string); ok {
			base = b
		}
		exit, ok := decl["expected_exit"].(int64)
		if !ok {
			t.Fatalf("%s: expected_exit missing", m)
		}
		var expected []string
		list, _ := decl["expected_failures"].([]any)
		for _, e := range list {
			f := e.(map[string]any)
			expected = append(expected, f["check"].(string)+":"+f["code"].(string))
		}
		sort.Strings(expected)
		states = append(states, state{name: "mutant " + filepath.Base(filepath.Dir(m)), base: base,
			mutant: filepath.Dir(m), exit: int(exit), failures: expected})
	}
	if len(states) < 5 {
		t.Fatalf("%s: only %d states found", labDir, len(states))
	}
	return states
}

// prepare turns a workspace fresh from setup into the state s.
func prepare(t *testing.T, labDir string, s state, ws string) {
	t.Helper()
	if s.base == "solution" {
		makeTarget(t, filepath.Join(labDir, "solution"), "solve", ws)
	}
	if s.mutant != "" {
		applyMutant(t, s.mutant, ws)
	}
}

// compareWithReference runs the reference executor, then Verify, on ws with
// the network cut and the same seed, and checks that both give the same
// result and the same exit code.
func compareWithReference(t *testing.T, reference string, lab *Lab, labDir, ws, name string) *Result {
	t.Helper()
	start := time.Now()
	cmd := exec.Command("python3", reference, "check", labDir, "--workspace", ws, "--json", "--no-network",
		"--seed", strconv.Itoa(conformanceSeed))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	pyExit := 0
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		pyExit = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("%s: python3: %v", name, runErr)
	}
	pyTime := time.Since(start)

	start = time.Now()
	r, err := Verify(context.Background(), lab, ws, conformanceSeed, true)
	if err != nil {
		t.Fatalf("%s: Verify: %v", name, err)
	}
	goTime := time.Since(start)
	goJSON, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}

	want, got := comparable(t, stdout.Bytes()), comparable(t, goJSON)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: JSON differs\nsagnac_local: %s\nGo:           %s\nstderr: %s", name, stdout.String(), goJSON, stderr.String())
	}
	if r.ExitCode() != pyExit {
		t.Errorf("%s: exit code %d, sagnac_local %d", name, r.ExitCode(), pyExit)
	}
	t.Logf("%-40s exit %d (sagnac_local %d)  failures %v  [python %s, go %s]", name, r.ExitCode(), pyExit,
		failures(r), pyTime.Round(time.Millisecond), goTime.Round(time.Millisecond))
	return r
}

// crossCheckCluster compares both executors on a lab with a cluster without
// any cluster: make setup would create it, and clusters never run on a
// workstation of the project. HOME is a directory of the test, so neither
// executor sees the cache of a real cluster. The workspace comes from
// starter/ (lib/espace.sh, the second half of make setup), then solve and
// the mutants. Three situations, each with its expected codes:
//   - no cluster: the cleanup finds no API and lets the checks run; they
//     give the preconditions of the lab (CLUSTER_MISSING), never an error of
//     the oracle;
//   - busy: another check holds check.lock, every check gives CLUSTER_BUSY;
//   - dirty: a stand-in kubectl answers and always lists a leftover pod (and
//     sleep returns at once), every check gives CLUSTER_DIRTY.
//
// The checks against a real cluster run on a VM of the factory
// (docs/contrat.md, section 7, and testdata/README.md).
func crossCheckCluster(t *testing.T, reference, labDir string, lab *Lab) {
	cadre := filepath.Dir(filepath.Dir(lab.libPath))
	tmp := t.TempDir()
	workspace := func(i int, s state) string {
		ws := filepath.Join(tmp, "s"+strconv.Itoa(i))
		cmd := exec.Command("sh", filepath.Join(cadre, "lib", "espace.sh"), filepath.Join(labDir, "starter"), ws, lab.ID)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("espace.sh: %v\n%s", err, out)
		}
		prepare(t, labDir, s, ws)
		return ws
	}
	states := labStates(t, labDir)

	t.Run("no cluster", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		for i, s := range states {
			r := compareWithReference(t, reference, lab, labDir, workspace(i, s), s.name)
			if r.ExitCode() == 0 || r.ExitCode() == 3 {
				t.Errorf("%s: exit code %d without a cluster", s.name, r.ExitCode())
			}
		}
	})

	solution := states[1]
	cache := func(t *testing.T) string {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".cache", "sagnac", "clusters", lab.Cluster)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "check.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	only := func(t *testing.T, r *Result, code string) {
		for _, c := range r.Checks {
			if c.Status != statusError || deref(c.Code) != code || deref(c.Origin) != originPlatform {
				t.Errorf("%s: %s %s %s, want %s", c.ID, c.Status, deref(c.Code), deref(c.Origin), code)
			}
		}
	}

	t.Run("busy", func(t *testing.T) {
		dir := cache(t)
		f, err := os.Open(filepath.Join(dir, "check.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		only(t, compareWithReference(t, reference, lab, labDir, workspace(len(states), solution), "busy"), codeClusterBusy)
	})

	t.Run("dirty", func(t *testing.T) {
		dir := cache(t)
		kubectl := "#!/bin/sh\ncase \"$*\" in *readyz*) exit 0 ;; *\"-o name\"*) echo pod/leftover ;; esac\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kubeconfig"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "sleep"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		only(t, compareWithReference(t, reference, lab, labDir, workspace(len(states)+1, solution), "dirty"), codeClusterDirty)
	})
}

// comparable decodes a result and drops what changes from one run to the
// next: started_at, every duration_ms and cli.
func comparable(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	delete(m, "started_at")
	delete(m, "duration_ms")
	delete(m, "cli")
	if list, ok := m["checks"].([]any); ok {
		for _, c := range list {
			delete(c.(map[string]any), "duration_ms")
		}
	}
	return m
}

// failures lists the failed or erroneous checks as id:code, sorted like the
// harness does.
func failures(r *Result) []string {
	var out []string
	for _, c := range r.Checks {
		if c.Code != nil {
			out = append(out, c.ID+":"+*c.Code)
		}
	}
	sort.Strings(out)
	return out
}

// makeTarget runs make like the lab build harness does: silent, offline,
// WORKSPACE given.
func makeTarget(t *testing.T, dir, target, ws string) {
	t.Helper()
	cmd := exec.Command("make", "-s", "-C", dir, target, "WORKSPACE="+ws)
	cmd.Env = append(os.Environ(), "SAGNAC_OFFLINE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make %s: %v\n%s", target, err, out)
	}
}

// applyMutant copies fichiers/ of a wrong solution over the workspace (modes
// and times kept), then runs its appliquer.sh with WORKSPACE set.
func applyMutant(t *testing.T, dir, ws string) {
	t.Helper()
	files := filepath.Join(dir, "fichiers")
	if info, err := os.Stat(files); err == nil && info.IsDir() {
		err := filepath.WalkDir(files, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(files, p)
			target := filepath.Join(ws, rel)
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
					return err
				}
				return os.Chmod(target, info.Mode().Perm())
			}
			if err := copyFile(p, target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(target, info.ModTime(), info.ModTime())
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "appliquer.sh")
	if _, err := os.Stat(script); err == nil {
		cmd := exec.Command("/bin/sh", script)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(), "WORKSPACE="+ws)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("appliquer.sh: %v\n%s", err, out)
		}
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
