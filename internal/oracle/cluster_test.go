package oracle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clusterMeta is the head of a lab with a cluster (docs/contrat.md, section
// 7): cluster, docker: true, the five tools and the two preconditions.
const clusterMeta = `id: x
version: 1
cluster: sagnac-b1
docker: true
tools: [k3d, kubectl, k3s, k3s-airgap, k3d-tools, busybox]
preconditions:
  - code: CLUSTER_MISSING
    description: "cluster absent"
  - code: CLUSTER_DIRTY
    description: "objets restants"
  - code: CLUSTER_BUSY
    description: "cluster occupé"
checks:
  - id: verify_one
    step: 1
    script: checks/one.sh
    codes: [ONE_BAD]
    preconditions: [CLUSTER_MISSING]
`

// clusterFixture is fixture plus _cadre/clusters/sagnac-b1.conf and the
// cleanup script of the cadre (a stub that records its calls).
func clusterFixture(t *testing.T, meta string, scripts map[string]string) (string, string) {
	t.Helper()
	dir, ws := fixture(t, meta, scripts, "ONE_BAD", "TWO_BAD", "CLUSTER_MISSING", "CLUSTER_DIRTY", "CLUSTER_BUSY")
	cadre := filepath.Join(filepath.Dir(dir), "_cadre")
	writeFile(t, filepath.Join(cadre, "clusters", "sagnac-b1.conf"),
		"# Cluster de référence\nAPI_PORT=6550\nAPI_PORT=7000\n", 0o644)
	writeFile(t, filepath.Join(cadre, "lib", "cluster-nettoyage.sh"), "#!/bin/sh\nexit 0\n", 0o755)
	return dir, ws
}

func writeFile(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadLabCluster(t *testing.T) {
	dir, _ := clusterFixture(t, clusterMeta, map[string]string{"one.sh": "sagnac_reussite"})
	lab := mustLoad(t, dir)
	if lab.Cluster != "sagnac-b1" || lab.APIPort != 6550 || !lab.Docker {
		t.Errorf("cluster %q, port %d, docker %v; want sagnac-b1, 6550 (first line wins), true", lab.Cluster, lab.APIPort, lab.Docker)
	}
	plain, _ := fixture(t, "id: x\nversion: 1\nchecks:"+okCheck, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
	if l := mustLoad(t, plain); l.Cluster != "" || l.APIPort != 0 {
		t.Errorf("lab without cluster: %q %d", l.Cluster, l.APIPort)
	}
}

// A lab with a cluster names a cluster of the cadre, declares docker: true,
// the five tools and the preconditions CLUSTER_DIRTY and CLUSTER_BUSY;
// anything else is ORACLE_INVALID_KIT (docs/contrat.md, section 7).
func TestLoadLabClusterRejects(t *testing.T) {
	cases := []struct {
		name, meta, conf, reason string
	}{
		{"not a string", strings.Replace(clusterMeta, "cluster: sagnac-b1", "cluster: [sagnac-b1]", 1), "", "name of a cluster"},
		{"unknown cluster", strings.Replace(clusterMeta, "cluster: sagnac-b1", "cluster: sagnac-zz", 1), "", "unknown"},
		{"invalid name", strings.Replace(clusterMeta, "cluster: sagnac-b1", "cluster: Sagnac", 1), "", "unknown"},
		{"path in the name", strings.Replace(clusterMeta, "cluster: sagnac-b1", "cluster: ../x", 1), "", "unknown"},
		{"no docker", strings.Replace(clusterMeta, "docker: true", "docker: false", 1), "", "docker: true"},
		{"docker absent", strings.Replace(clusterMeta, "docker: true\n", "", 1), "", "docker: true"},
		{"tool missing", strings.Replace(clusterMeta, "k3s-airgap, ", "", 1), "", "k3s-airgap"},
		{"no tools", strings.Replace(clusterMeta, "tools: [k3d, kubectl, k3s, k3s-airgap, k3d-tools, busybox]\n", "", 1), "", "k3d, kubectl"},
		{"CLUSTER_BUSY not declared", strings.Replace(clusterMeta, "  - code: CLUSTER_BUSY\n    description: \"cluster occupé\"\n", "", 1), "", "CLUSTER_BUSY"},
		{"CLUSTER_DIRTY not declared", strings.Replace(clusterMeta, "  - code: CLUSTER_DIRTY\n    description: \"objets restants\"\n", "", 1), "", "CLUSTER_DIRTY"},
		{"port missing", clusterMeta, "OTHER=1\n", "API_PORT"},
		{"port not a number", clusterMeta, "API_PORT=abc\n", "API_PORT"},
		{"port out of range", clusterMeta, "API_PORT=70000\n", "API_PORT"},
		{"port zero", clusterMeta, "API_PORT=0\n", "API_PORT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := clusterFixture(t, c.meta, map[string]string{"one.sh": "sagnac_reussite"})
			if c.conf != "" {
				writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", "sagnac-b1.conf"), c.conf, 0o644)
			}
			_, err := LoadLab(dir)
			var kit *KitError
			if !errors.As(err, &kit) {
				t.Fatalf("LoadLab error = %v, want a KitError", err)
			}
			if !strings.Contains(kit.Error(), c.reason) {
				t.Errorf("error %q does not name %q", kit.Error(), c.reason)
			}
		})
	}
}

// checks/teardown.sh, when present, is an executable file.
func TestLoadLabTeardown(t *testing.T) {
	meta := "id: x\nversion: 1\nchecks:" + okCheck
	dir, _ := fixture(t, meta, map[string]string{"one.sh": "sagnac_reussite", "teardown.sh": ": teardown"}, "ONE_BAD")
	if lab := mustLoad(t, dir); lab.Teardown != filepath.Join(lab.Dir, "checks", "teardown.sh") {
		t.Errorf("Teardown = %q", lab.Teardown)
	}
	if err := os.Chmod(filepath.Join(dir, "checks", "teardown.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	var kit *KitError
	if _, err := LoadLab(dir); !errors.As(err, &kit) || !strings.Contains(kit.Error(), "teardown.sh") {
		t.Fatalf("LoadLab error = %v, want a KitError on teardown.sh", err)
	}
	if err := os.Remove(filepath.Join(dir, "checks", "teardown.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "checks", "teardown.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLab(dir); !errors.As(err, &kit) {
		t.Fatalf("LoadLab error = %v, want a KitError on a teardown.sh directory", err)
	}
	if err := os.Remove(filepath.Join(dir, "checks", "teardown.sh")); err != nil {
		t.Fatal(err)
	}
	if lab := mustLoad(t, dir); lab.Teardown != "" {
		t.Errorf("Teardown = %q without the file", lab.Teardown)
	}
}
