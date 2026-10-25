package cleanup

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// cacheWith builds a cache with what Sagnac keeps (models, binaries of the
// tools, base images) and what it can drop (clusters, half-written files).
func cacheWith(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sagnac")
	write(t, filepath.Join(root, "qwen3-0.6b-q4_0", "Qwen3-0.6B-Q4_0.gguf"), "model")
	write(t, filepath.Join(root, "k3s-airgap", "k3s-airgap-images.tar"), "images")
	write(t, filepath.Join(root, "k3d", "v5.8.3", "k3d"), "bin")
	write(t, filepath.Join(root, "clusters", "sagnac-b1", "kubeconfig"), "k")
	write(t, filepath.Join(root, "clusters", "sagnac-b3", "kubeconfig"), "k")
	write(t, filepath.Join(root, "llama-cpp", "llama.tar.gz.partiel.4242"), "half")
	write(t, filepath.Join(root, "kubectl", ".partiel-not-a-temp"), "keep")
	return root
}

func TestRootRefusesEmptyAndUnsafe(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"nothing set", map[string]string{}},
		{"empty cache, empty home", map[string]string{"SAGNAC_CACHE": "", "HOME": ""}},
		{"relative cache", map[string]string{"SAGNAC_CACHE": "cache/sagnac", "HOME": home}},
		{"filesystem root", map[string]string{"SAGNAC_CACHE": "/", "HOME": home}},
		{"home itself", map[string]string{"SAGNAC_CACHE": home, "HOME": home}},
		{"dot dot to the root", map[string]string{"SAGNAC_CACHE": home + "/../..", "HOME": home}},
	}
	for _, c := range cases {
		getenv := func(k string) string { return c.env[k] }
		if root, err := Root(getenv); err == nil {
			t.Errorf("%s: Root = %q, want an error", c.name, root)
		}
	}
}

func TestRootDefaultsAndResolves(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The default is $HOME/.cache/sagnac, whatever XDG_CACHE_HOME says: it is
	// the cache of the lab framework, which ignores XDG.
	getenv := func(k string) string {
		return map[string]string{"HOME": home, "XDG_CACHE_HOME": "/elsewhere"}[k]
	}
	got, err := Root(getenv)
	if err != nil || got != filepath.Join(home, ".cache", "sagnac") {
		t.Errorf("Root = %q, %v", got, err)
	}
	// A symbolic link in the path is resolved: the checks below compare real paths.
	real := filepath.Join(home, "real")
	link := filepath.Join(home, "link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	getenv = func(k string) string { return map[string]string{"HOME": home, "SAGNAC_CACHE": link + "/sagnac"}[k] }
	if got, err := Root(getenv); err != nil || got != filepath.Join(real, "sagnac") {
		t.Errorf("Root through a link = %q, %v, want %q", got, err, filepath.Join(real, "sagnac"))
	}
}

func TestValidClusterName(t *testing.T) {
	for _, ok := range []string{"sagnac-b1", "a", "sagnac-essai-v3", "0x"} {
		if !ValidCluster(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a/b", "..", "a b", "A", "a.b", "a\n"} {
		if ValidCluster(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestPlanOneCluster(t *testing.T) {
	root := cacheWith(t)
	plan, err := Build(root, []string{"sagnac-b1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Clusters, []Cluster{{Name: "sagnac-b1", State: filepath.Join(root, "clusters", "sagnac-b1"), Exists: true}}) {
		t.Errorf("clusters = %+v", plan.Clusters)
	}
	if want := []string{filepath.Join(root, "llama-cpp", "llama.tar.gz.partiel.4242")}; !reflect.DeepEqual(plan.Temporary, want) {
		t.Errorf("temporary = %v, want %v", plan.Temporary, want)
	}
}

func TestPlanAllClusters(t *testing.T) {
	root := cacheWith(t)
	plan, err := Build(root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range plan.Clusters {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"sagnac-b1", "sagnac-b3"}) {
		t.Errorf("clusters = %v", names)
	}
}

func TestPlanClusterWithoutState(t *testing.T) {
	// A cluster named by the lab and never created: still planned (its
	// containers may exist), with the state directory that would be removed.
	root := cacheWith(t)
	plan, err := Build(root, []string{"sagnac-b9"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Clusters) != 1 || plan.Clusters[0].Exists {
		t.Errorf("clusters = %+v, want one without state", plan.Clusters)
	}
}

func TestPlanRefusesBadNames(t *testing.T) {
	root := cacheWith(t)
	for _, name := range []string{"", "../x", "a/b", "..", "-rf"} {
		if _, err := Build(root, []string{name}, false); !errors.Is(err, ErrUnsafe) {
			t.Errorf("name %q: err = %v, want ErrUnsafe", name, err)
		}
	}
}

func TestPlanRefusesEmptyRoot(t *testing.T) {
	if _, err := Build("", []string{"sagnac-b1"}, false); !errors.Is(err, ErrUnsafe) {
		t.Errorf("empty root: err = %v, want ErrUnsafe", err)
	}
	if _, err := Build("relative/dir", nil, true); !errors.Is(err, ErrUnsafe) {
		t.Errorf("relative root: err = %v, want ErrUnsafe", err)
	}
}

func TestPlanRefusesLinkOutsideTheCache(t *testing.T) {
	root := cacheWith(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "precious"), "keep")
	if err := os.Symlink(outside, filepath.Join(root, "clusters", "evil")); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Clusters {
		if c.Name == "evil" {
			t.Errorf("a link out of the cache must not be planned: %+v", c)
		}
	}
	if !reflect.DeepEqual(plan.Refused, []string{filepath.Join(root, "clusters", "evil")}) {
		t.Errorf("refused = %v", plan.Refused)
	}
	// Named directly, the same link is refused with an error.
	if _, err := Build(root, []string{"evil"}, false); !errors.Is(err, ErrUnsafe) {
		t.Errorf("named link: err = %v, want ErrUnsafe", err)
	}
	if !exists(filepath.Join(outside, "precious")) {
		t.Error("the target of the link was touched")
	}
}

func TestPlanRefusesClustersDirectoryLinkedOutside(t *testing.T) {
	root := cacheWith(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "other", "x"), "keep")
	if err := os.RemoveAll(filepath.Join(root, "clusters")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "clusters")); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(root, nil, true); !errors.Is(err, ErrUnsafe) {
		t.Errorf("err = %v, want ErrUnsafe", err)
	}
}

func TestRemoveTemporaryKeepsEverythingElse(t *testing.T) {
	root := cacheWith(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "precious"), "keep")
	// A half-written entry that is a link: the link goes, not its target.
	link := filepath.Join(root, "k3d", "k3d.tar.partiel.7")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveTemporary(root, plan.Temporary); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{filepath.Join(root, "llama-cpp", "llama.tar.gz.partiel.4242"), link} {
		if exists(gone) {
			t.Errorf("%s should be removed", gone)
		}
	}
	for _, kept := range []string{
		filepath.Join(root, "qwen3-0.6b-q4_0", "Qwen3-0.6B-Q4_0.gguf"),
		filepath.Join(root, "k3s-airgap", "k3s-airgap-images.tar"),
		filepath.Join(root, "k3d", "v5.8.3", "k3d"),
		filepath.Join(root, "kubectl", ".partiel-not-a-temp"),
		filepath.Join(root, "clusters", "sagnac-b1", "kubeconfig"),
		filepath.Join(outside, "precious"),
	} {
		if !exists(kept) {
			t.Errorf("%s should be kept", kept)
		}
	}
}

func TestRemoveTemporaryRefusesPathsOutsideTheCache(t *testing.T) {
	root := cacheWith(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "x.partiel.1")
	write(t, victim, "keep")
	for _, p := range []string{"", victim, filepath.Join(root, "..", "x.partiel.1"), root, filepath.Join(root, "clusters")} {
		if err := RemoveTemporary(root, []string{p}); !errors.Is(err, ErrUnsafe) {
			t.Errorf("path %q: err = %v, want ErrUnsafe", p, err)
		}
	}
	if !exists(victim) {
		t.Error("a file out of the cache was removed")
	}
}

func TestTemporaryScanStaysShallow(t *testing.T) {
	root := cacheWith(t)
	// Deep inside an extracted tool, a name that looks temporary is not ours.
	write(t, filepath.Join(root, "llama-cpp", "b11706", "lib", "deep", "x.partiel.1"), "keep")
	plan, err := Build(root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plan.Temporary {
		if filepath.Base(p) == "x.partiel.1" {
			t.Errorf("deep entry planned: %s", p)
		}
	}
}

func TestLabTemporary(t *testing.T) {
	lab := t.TempDir()
	write(t, filepath.Join(lab, "workspace", "main.py"), "work of the student")
	write(t, filepath.Join(lab, ".workspace.partiel.99", "x"), "half")
	write(t, filepath.Join(lab, "starter", "a"), "a")
	got, err := LabTemporary(lab)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(lab, ".workspace.partiel.99")}; !reflect.DeepEqual(got, want) {
		t.Errorf("LabTemporary = %v, want %v", got, want)
	}
	if err := RemoveTemporary(lab, got); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(lab, ".workspace.partiel.99")) || !exists(filepath.Join(lab, "workspace", "main.py")) {
		t.Error("only the partial copy must go; the workspace is the student's work")
	}
}
