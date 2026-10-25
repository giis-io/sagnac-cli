package oracle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredCluster(t *testing.T) {
	cases := []struct {
		name, meta, want string
		fails            bool
	}{
		{"none", "id: hello\nversion: 1\n", "", false},
		{"named", "id: m2-l2\nversion: 1\ncluster: sagnac-b1\ndocker: true\n", "sagnac-b1", false},
		{"quoted", "cluster: \"sagnac-b3\"\n", "sagnac-b3", false},
		{"not a string", "cluster: 3\n", "", true},
		{"not yaml", "cluster: [\n", "", true},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte(c.meta), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := DeclaredCluster(dir)
		if (err != nil) != c.fails || got != c.want {
			t.Errorf("%s: DeclaredCluster = %q, %v", c.name, got, err)
		}
	}
	if _, err := DeclaredCluster(t.TempDir()); err == nil {
		t.Error("a directory without meta.yaml must be refused")
	}
}
