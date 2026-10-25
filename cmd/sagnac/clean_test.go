package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// fakeCluster stands for _cadre/bin/sagnac-cluster: it records each call and
// removes the state directory like the real "delete" does.
const fakeCluster = `#!/bin/sh
echo "$1 $2 cache=$SAGNAC_CACHE" >> "$(dirname "$0")/calls"
[ -f "$(dirname "$0")/fail" ] && exit 2
[ "$1" = delete ] && rm -rf "$SAGNAC_CACHE/clusters/$2"
exit 0
`

type cleanRig struct {
	cache string // the Sagnac cache
	work  string // the folder where kits were pulled
	calls string // calls recorded by the fake script
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func present(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// newCleanRig gives a cache with two clusters, a model, base images and a
// half-written download, and a folder with the kit of a lab with a cluster, a
// lab without one, and the framework script.
func newCleanRig(t *testing.T, withScript bool) *cleanRig {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &cleanRig{cache: filepath.Join(home, ".cache", "sagnac"), work: filepath.Join(home, "labs")}
	t.Setenv("HOME", home)
	t.Setenv("SAGNAC_CACHE", r.cache)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	for _, f := range []string{
		"qwen3-0.6b-q4_0/Qwen3-0.6B-Q4_0.gguf", "k3s-airgap/k3s-airgap-images.tar", "k3d/v5/k3d",
		"clusters/sagnac-b1/kubeconfig", "clusters/sagnac-b3/kubeconfig", "llama-cpp/llama.tar.gz.partiel.42",
	} {
		writeFile(t, filepath.Join(r.cache, f), "x", 0o644)
	}
	writeFile(t, filepath.Join(r.work, "m2-l2", "meta.yaml"), "id: m2-l2\nversion: 1\ncluster: sagnac-b1\n", 0o644)
	writeFile(t, filepath.Join(r.work, "m2-l2", "workspace", "answer.yaml"), "work", 0o644)
	writeFile(t, filepath.Join(r.work, "hello", "meta.yaml"), "id: hello\nversion: 1\n", 0o644)
	writeFile(t, filepath.Join(r.work, "hello", "workspace", "ok"), "work", 0o644)
	if withScript {
		writeFile(t, filepath.Join(r.work, "_cadre", "bin", "sagnac-cluster"), fakeCluster, 0o755)
	}
	r.calls = filepath.Join(r.work, "_cadre", "bin", "calls")
	return r
}

func (r *cleanRig) recorded(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(r.calls)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (r *cleanRig) kept(t *testing.T) {
	t.Helper()
	for _, f := range []string{
		"qwen3-0.6b-q4_0/Qwen3-0.6B-Q4_0.gguf", "k3s-airgap/k3s-airgap-images.tar", "k3d/v5/k3d",
	} {
		if !present(filepath.Join(r.cache, f)) {
			t.Errorf("%s must stay in the cache", f)
		}
	}
	for _, f := range []string{"m2-l2/workspace/answer.yaml", "hello/workspace/ok"} {
		if !present(filepath.Join(r.work, f)) {
			t.Errorf("%s is the student's work and must stay", f)
		}
	}
}

func TestCleanDryRunDeletesNothing(t *testing.T) {
	r := newCleanRig(t, true)
	t.Chdir(filepath.Join(r.work, "m2-l2"))
	code, out, errOut := execute(t, "clean", "--dry-run")
	if code != exitOK {
		t.Fatalf("code %d (%s)", code, errOut)
	}
	state := filepath.Join(r.cache, "clusters", "sagnac-b1")
	for _, want := range []string{
		i18n.T("clean.dry_run"),
		i18n.T("clean.would_cluster", "sagnac-b1", state),
		i18n.T("clean.would_temporary", filepath.Join(r.cache, "llama-cpp", "llama.tar.gz.partiel.42")),
		i18n.T("clean.kept", r.cache),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sagnac-b3") {
		t.Errorf("the other cluster is not this lab's:\n%s", out)
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("dry run called the script: %v", got)
	}
	for _, f := range []string{"clusters/sagnac-b1/kubeconfig", "clusters/sagnac-b3/kubeconfig", "llama-cpp/llama.tar.gz.partiel.42"} {
		if !present(filepath.Join(r.cache, f)) {
			t.Errorf("dry run removed %s", f)
		}
	}
	r.kept(t)
}

func TestCleanCurrentLab(t *testing.T) {
	r := newCleanRig(t, true)
	t.Chdir(filepath.Join(r.work, "m2-l2", "workspace")) // from workspace/, like check
	code, out, errOut := execute(t, "clean")
	if code != exitOK {
		t.Fatalf("code %d (%s)", code, errOut)
	}
	if got := r.recorded(t); len(got) != 1 || got[0] != "delete sagnac-b1 cache="+r.cache {
		t.Errorf("script calls = %v", got)
	}
	if present(filepath.Join(r.cache, "clusters", "sagnac-b1")) {
		t.Error("the cluster state must be gone")
	}
	if !present(filepath.Join(r.cache, "clusters", "sagnac-b3")) {
		t.Error("the cluster of another lab must stay without --all")
	}
	if present(filepath.Join(r.cache, "llama-cpp", "llama.tar.gz.partiel.42")) {
		t.Error("the half-written download must be gone")
	}
	if !strings.Contains(out, i18n.T("clean.cluster_done", "sagnac-b1")) {
		t.Errorf("output lacks the cluster line:\n%s", out)
	}
	r.kept(t)
}

func TestCleanAllLabs(t *testing.T) {
	r := newCleanRig(t, true)
	t.Chdir(r.work)
	if code, _, errOut := execute(t, "clean", "--all"); code != exitOK {
		t.Fatalf("code %d (%s)", code, errOut)
	}
	if got := r.recorded(t); len(got) != 2 || got[0] != "delete sagnac-b1 cache="+r.cache || got[1] != "delete sagnac-b3 cache="+r.cache {
		t.Errorf("script calls = %v", got)
	}
	if present(filepath.Join(r.cache, "clusters", "sagnac-b1")) || present(filepath.Join(r.cache, "clusters", "sagnac-b3")) {
		t.Error("every cluster must be gone")
	}
	r.kept(t)
}

func TestCleanAllDryRunFromAnywhere(t *testing.T) {
	r := newCleanRig(t, false) // --all needs no lab and, dry, no script
	t.Chdir(t.TempDir())
	code, out, errOut := execute(t, "clean", "--all", "--dry-run")
	if code != exitOK {
		t.Fatalf("code %d (%s)", code, errOut)
	}
	for _, name := range []string{"sagnac-b1", "sagnac-b3"} {
		if !strings.Contains(out, i18n.T("clean.would_cluster", name, filepath.Join(r.cache, "clusters", name))) {
			t.Errorf("output lacks %s:\n%s", name, out)
		}
	}
}

func TestCleanLabWithoutCluster(t *testing.T) {
	r := newCleanRig(t, true)
	if err := os.RemoveAll(filepath.Join(r.cache, "llama-cpp")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(r.work, "hello"))
	code, out, errOut := execute(t, "clean")
	if code != exitOK {
		t.Fatalf("code %d (%s)", code, errOut)
	}
	if !strings.Contains(out, i18n.T("clean.nothing")) {
		t.Errorf("output = %q", out)
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("script called for a lab without cluster: %v", got)
	}
	if !present(filepath.Join(r.cache, "clusters", "sagnac-b1")) {
		t.Error("another lab's cluster was removed")
	}
}

func TestCleanOutsideAnyLabNeedsAll(t *testing.T) {
	newCleanRig(t, true)
	t.Chdir(t.TempDir())
	code, _, errOut := execute(t, "clean")
	if code != exitUsage || !strings.Contains(errOut, i18n.T("check.no_lab", mustGetwd(t))) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCleanArguments(t *testing.T) {
	newCleanRig(t, true)
	cases := [][]string{{"clean", "--force"}, {"clean", "extra"}, {"clean", "--all", "extra"}}
	for _, args := range cases {
		if code, _, _ := execute(t, args...); code != exitUsage {
			t.Errorf("%v: code %d, want 64", args, code)
		}
	}
}

func TestCleanScriptMissingKeepsTheState(t *testing.T) {
	r := newCleanRig(t, false)
	t.Chdir(filepath.Join(r.work, "m2-l2"))
	code, _, errOut := execute(t, "clean")
	if code != exitFailure || !strings.Contains(errOut, i18n.T("clean.no_script", "sagnac-b1")) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	// Without the script the cluster would be left running with no way to
	// reach it: its state directory stays.
	if !present(filepath.Join(r.cache, "clusters", "sagnac-b1", "kubeconfig")) {
		t.Error("the state of a cluster that was not deleted must stay")
	}
}

func TestCleanScriptFailureKeepsTheState(t *testing.T) {
	r := newCleanRig(t, true)
	writeFile(t, filepath.Join(r.work, "_cadre", "bin", "fail"), "", 0o644)
	t.Chdir(filepath.Join(r.work, "m2-l2"))
	code, _, errOut := execute(t, "clean")
	if code != exitEnvironment || !strings.Contains(errOut, i18n.T("clean.cluster_failed", "sagnac-b1")) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	if !present(filepath.Join(r.cache, "clusters", "sagnac-b1", "kubeconfig")) {
		t.Error("the state must stay when the script fails")
	}
}

func TestCleanRefusesUnsafeCache(t *testing.T) {
	r := newCleanRig(t, true)
	t.Chdir(filepath.Join(r.work, "m2-l2"))
	home := os.Getenv("HOME")
	for name, env := range map[string]map[string]string{
		"filesystem root": {"SAGNAC_CACHE": "/"},
		"home":            {"SAGNAC_CACHE": home},
		"relative":        {"SAGNAC_CACHE": "cache"},
		"parent of home":  {"SAGNAC_CACHE": filepath.Dir(home)},
	} {
		for k, v := range env {
			t.Setenv(k, v)
		}
		code, _, errOut := execute(t, "clean", "--all")
		if code != exitFailure || !strings.Contains(errOut, i18n.T("clean.unsafe")) {
			t.Errorf("%s: code %d, stderr %q", name, code, errOut)
		}
	}
	t.Setenv("SAGNAC_CACHE", "")
	t.Setenv("HOME", "")
	code, _, errOut := execute(t, "clean", "--all")
	if code != exitFailure || !strings.Contains(errOut, i18n.T("clean.no_cache")) {
		t.Errorf("no home: code %d, stderr %q", code, errOut)
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("script called with an unsafe cache: %v", got)
	}
	r.kept(t)
}

func TestCleanRefusesHostileClusterName(t *testing.T) {
	r := newCleanRig(t, true)
	writeFile(t, filepath.Join(r.work, "m2-l2", "meta.yaml"), "id: m2-l2\nversion: 1\ncluster: ../../../etc\n", 0o644)
	t.Chdir(filepath.Join(r.work, "m2-l2"))
	code, _, errOut := execute(t, "clean")
	if code != exitFailure || !strings.Contains(errOut, i18n.T("clean.unsafe")) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	if got := r.recorded(t); got != nil {
		t.Errorf("script called with a hostile name: %v", got)
	}
}

func TestCleanRemovesPartialStarterCopyOnly(t *testing.T) {
	r := newCleanRig(t, true)
	writeFile(t, filepath.Join(r.work, "hello", ".workspace.partiel.7", "x"), "half", 0o644)
	t.Chdir(filepath.Join(r.work, "hello"))
	if code, _, errOut := execute(t, "clean"); code != exitOK {
		t.Fatalf("code %d (%s)", code, errOut)
	}
	if present(filepath.Join(r.work, "hello", ".workspace.partiel.7")) {
		t.Error("the partial copy must go")
	}
	r.kept(t)
}
