package oracle

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
		"# Cluster de référence\n\nAPI_PORT=6550\n", 0o644)
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
	if lab.Cluster != "sagnac-b1" || lab.APIPort != 6550 || !lab.Docker || lab.ClusterContract != 2 {
		t.Errorf("cluster %q, port %d, docker %v, contract %d; want sagnac-b1, 6550, true, 2",
			lab.Cluster, lab.APIPort, lab.Docker, lab.ClusterContract)
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
		{"port missing", clusterMeta, "# CONTRAT absent\n", "invalid API_PORT"},
		{"port empty", clusterMeta, "API_PORT=\n", "invalid API_PORT"},
		{"port with a blank", clusterMeta, "API_PORT=6550 \n", "invalid API_PORT"},
		{"port twice", clusterMeta, "API_PORT=6550\nAPI_PORT=7000\n", `unknown line "API_PORT=7000"`},
		{"unknown key", clusterMeta, "API_PORT=6550\nOTHER=1\n", `unknown line "OTHER=1"`},
		{"key with a blank", clusterMeta, "API_PORT = 6550\n", "unknown line"},
		{"indented key", clusterMeta, " API_PORT=6550\n", "unknown line"},
		{"indented comment", clusterMeta, "API_PORT=6550\n  # note\n", "unknown line"},
		{"line without =", clusterMeta, "API_PORT=6550\nCONTRAT\n", "unknown line"},
		{"lower case key", clusterMeta, "api_port=6550\n", "unknown line"},
		{"unknown contract", clusterMeta, "API_PORT=6550\nCONTRAT=9\n", "CONTRAT=9 unknown"},
		{"contract 2 written", clusterMeta, "API_PORT=6550\nCONTRAT=2\n", "CONTRAT=2 unknown"},
		{"empty contract", clusterMeta, "API_PORT=6550\nCONTRAT=\n", "CONTRAT= unknown"},
		{"contract with a blank", clusterMeta, "API_PORT=6550\nCONTRAT=3 \n", "CONTRAT=3  unknown"},
		{"contract twice", clusterMeta, "API_PORT=6550\nCONTRAT=3\nCONTRAT=3\n", `unknown line "CONTRAT=3"`},
		{"not UTF-8", clusterMeta, "API_PORT=6550\n# \xff\n", "unreadable"},
		{"port not a number", clusterMeta, "API_PORT=abc\n", "API_PORT"},
		{"port out of range", clusterMeta, "API_PORT=70000\n", "API_PORT"},
		{"port zero", clusterMeta, "API_PORT=0\n", "API_PORT"},
		{"models on a cluster v2", clusterMeta, "API_PORT=6599\nMODELES=qwen3-0.6b-q4_0\n", "MODELES requires CONTRAT=3"},
		{"models empty", clusterMeta, "API_PORT=6599\nCONTRAT=3\nMODELES=\n", "invalid MODELES"},
		{"models with a path", clusterMeta, "API_PORT=6599\nCONTRAT=3\nMODELES=../etc,a\n", "invalid MODELES"},
		{"models twice", clusterMeta, "API_PORT=6599\nCONTRAT=3\nMODELES=a\nMODELES=b\n", `unknown line "MODELES=b"`},
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

// The contract of the cluster is read in its .conf: 2 without the line
// CONTRAT, 3 with CONTRAT=3 (docs/contrat.md, section 7 bis). Comments, empty
// lines, blank lines and CRLF ends are skipped, in any order of the keys.
func TestLoadLabClusterContract(t *testing.T) {
	for _, c := range []struct {
		name, conf string
		port, want int
	}{
		{"v2", "API_PORT=6550\n", 6550, 2},
		{"v3", "# Premier cluster du contrat v3\nAPI_PORT=6560\nCONTRAT=3\n", 6560, 3},
		{"v3, contract first", "CONTRAT=3\n\n   \nAPI_PORT=6552", 6552, 3},
		{"v3, CRLF", "# essai\r\nAPI_PORT=6560\r\nCONTRAT=3\r\n", 6560, 3},
		{"leading zero", "API_PORT=06550\n", 6550, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := clusterFixture(t, clusterMeta, map[string]string{"one.sh": "sagnac_reussite"})
			writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", "sagnac-b1.conf"), c.conf, 0o644)
			lab := mustLoad(t, dir)
			if lab.APIPort != c.port || lab.ClusterContract != c.want {
				t.Errorf("port %d, contract %d; want %d, %d", lab.APIPort, lab.ClusterContract, c.port, c.want)
			}
		})
	}
}

// Confs of the clusters of B4 and B5, as the cadre ships them.
const (
	confB4 = `# Cluster de la brique B4 (docs/programme/B4/README.md), contrat v3 (ADR-007, CONTRAT.md § 7 quater). Un seul
# cluster Sagnac en marche à la fois : make setup arrête les autres. Lu par bin/sagnac-cluster et sagnac_local.py.
# MODELES (C1) : fichiers GGUF montés en lecture seule dans le nœud (/var/lib/sagnac/modeles), quand le lab les
# cite dans tools (M3 seulement) ; le routeur de llama-server les lit par un volume hostPath posé par l'administrateur.
API_PORT=6580
CONTRAT=3
MODELES=qwen3-0.6b-q4_0,qwen3-1.7b-q4_k_m
`
	confB5 = `# Cluster de la brique B5 (contrat v3, ADR-007, CONTRAT.md § 7 quater ; changement C3 du plan de B5). Un seul
# cluster Sagnac en marche à la fois : make setup arrête les autres, dont sagnac-b3. Kyverno (admission seule) et
# le registre local sont posés par make setup quand le lab cite les outils kyverno et registre-oci.
API_PORT=6570
CONTRAT=3
`
	confEssaiB5 = `# Cluster des tests du socle S2 de B5 (labs/_cadre/tests/test_cluster.py, TestClusterB5), usine seulement.
API_PORT=6553
CONTRAT=3
`
)

// The confs of B4 and B5 and the cases of test_conf_cluster_cles_fermees of
// the cadre: MODELES (C1 of B4) is a list of tools, on a cluster v3 only;
// sagnac-b5 and sagnac-essai-b5 (C3 of B5) add no key. A conf the CLI
// refuses is ORACLE_INVALID_KIT, code 3, like bin/sagnac-cluster.
func TestReadClusterConfB4B5(t *testing.T) {
	for _, c := range []struct {
		name, conf string
		want       clusterConf
		reason     string
	}{
		{"sagnac-b4", confB4, clusterConf{6580, 3, []string{"qwen3-0.6b-q4_0", "qwen3-1.7b-q4_k_m"}}, ""},
		{"sagnac-b5", confB5, clusterConf{6570, 3, nil}, ""},
		{"sagnac-essai-b5", confEssaiB5, clusterConf{6553, 3, nil}, ""},
		{"sagnac-zz-un-modele", "API_PORT=6599\nCONTRAT=3\nMODELES=qwen3-0.6b-q4_0\n", clusterConf{6599, 3, []string{"qwen3-0.6b-q4_0"}}, ""},
		{"sagnac-zz-modeles-avant", "MODELES=a.b_c-d,e\nCONTRAT=3\nAPI_PORT=6599\n", clusterConf{6599, 3, []string{"a.b_c-d", "e"}}, ""},
		{"sagnac-zz-contrat9", "API_PORT=6599\nCONTRAT=9\n", clusterConf{}, "CONTRAT=9 unknown"},
		{"sagnac-zz-cle", "API_PORT=6599\nRUNSC=1\n", clusterConf{}, `unknown line "RUNSC=1" (keys: API_PORT, CONTRAT, MODELES)`},
		{"sagnac-zz-modeles-v2", "API_PORT=6599\nMODELES=qwen3-0.6b-q4_0\n", clusterConf{}, "MODELES requires CONTRAT=3"},
		{"sagnac-zz-modeles-vide", "API_PORT=6599\nCONTRAT=3\nMODELES=\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-modeles-chemin", "API_PORT=6599\nCONTRAT=3\nMODELES=../etc,a\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-virgule-avant", "API_PORT=6599\nCONTRAT=3\nMODELES=,a\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-virgule-apres", "API_PORT=6599\nCONTRAT=3\nMODELES=a,\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-deux-virgules", "API_PORT=6599\nCONTRAT=3\nMODELES=a,,b\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-modeles-blanc", "API_PORT=6599\nCONTRAT=3\nMODELES=a, b\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-majuscules", "API_PORT=6599\nCONTRAT=3\nMODELES=Qwen\n", clusterConf{}, "invalid MODELES"},
		{"sagnac-zz-modeles-contrat9", "API_PORT=6599\nCONTRAT=9\nMODELES=a\n", clusterConf{}, "CONTRAT=9 unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cadre := t.TempDir()
			writeFile(t, filepath.Join(cadre, "clusters", c.name+".conf"), c.conf, 0o644)
			got, err := readClusterConf(cadre, c.name)
			if c.reason == "" {
				if err != nil {
					t.Fatalf("readClusterConf: %v", err)
				}
				if got.apiPort != c.want.apiPort || got.contract != c.want.contract || !slices.Equal(got.models, c.want.models) {
					t.Errorf("conf %+v, want %+v", got, c.want)
				}
				return
			}
			var kit *KitError
			if !errors.As(err, &kit) {
				t.Fatalf("readClusterConf error = %v, want a KitError", err)
			}
			if !strings.Contains(kit.Error(), c.reason) {
				t.Errorf("error %q does not name %q", kit.Error(), c.reason)
			}
		})
	}
}

// A lab on the cluster of B4 loads with the models of its conf.
func TestLoadLabClusterModels(t *testing.T) {
	meta := strings.Replace(clusterMeta, "cluster: sagnac-b1", "cluster: sagnac-b4", 1)
	dir, _ := clusterFixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"})
	writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", "sagnac-b4.conf"), confB4, 0o644)
	lab := mustLoad(t, dir)
	if lab.Cluster != "sagnac-b4" || lab.APIPort != 6580 || lab.ClusterContract != 3 ||
		!slices.Equal(lab.ClusterModels, []string{"qwen3-0.6b-q4_0", "qwen3-1.7b-q4_k_m"}) {
		t.Errorf("cluster %q, port %d, contract %d, models %v", lab.Cluster, lab.APIPort, lab.ClusterContract, lab.ClusterModels)
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

// clusterHome sets HOME to a new directory with the cache of the cluster
// sagnac-b1 and its check.lock, as make setup leaves them.
func clusterHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".cache", "sagnac", "clusters", "sagnac-b1", "check.lock"), "", 0o600)
	t.Setenv("HOME", home)
	return home
}

// recordingCleanup replaces the cleanup script of the cadre with one that
// appends "cleanup <rc>" and its environment to log, then exits with rc.
func recordingCleanup(t *testing.T, labDir, log string, rc int) {
	t.Helper()
	writeFile(t, filepath.Join(filepath.Dir(labDir), "_cadre", "lib", "cluster-nettoyage.sh"),
		"#!/bin/sh\necho \"cleanup lab=$SAGNAC_LAB cluster=$SAGNAC_CLUSTER check=${SAGNAC_CHECK-unset} lib=${SAGNAC_LIB##*/} pwd=$(pwd -P)\" >>"+log+"\nexit "+strconv.Itoa(rc)+"\n", 0o755)
}

func logLines(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func codes(r *Result) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Status+":"+deref(c.Code)+":"+deref(c.Origin))
	}
	return out
}

// The cleanup of the cluster runs before the first check and after the
// last one, with the environment of the checks (SAGNAC_CLUSTER, no
// SAGNAC_CHECK), outside duration_ms; the checks see SAGNAC_CLUSTER.
func TestVerifyClusterCleanup(t *testing.T) {
	clusterHome(t)
	dir, ws := clusterFixture(t, clusterMeta, map[string]string{
		"one.sh": `[ "$SAGNAC_CLUSTER" = sagnac-b1 ] || sagnac_echec ONE_BAD
echo check >>"$SAGNAC_WORKSPACE/../calls"
sagnac_reussite`})
	log := filepath.Join(filepath.Dir(ws), "calls")
	recordingCleanup(t, dir, log, 0)
	cleanup := filepath.Join(filepath.Dir(dir), "_cadre", "lib", "cluster-nettoyage.sh")
	raw, _ := os.ReadFile(cleanup)
	writeFile(t, cleanup, strings.Replace(string(raw), "exit 0", "sleep 1\nexit 0", 1), 0o755)
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode() != 0 {
		t.Fatalf("checks %v, want pass", codes(r))
	}
	wantCleanup := "cleanup lab=x cluster=sagnac-b1 check=unset lib=controle.sh pwd=" + resolve(ws)
	if got := logLines(t, log); !slices.Equal(got, []string{wantCleanup, "check", wantCleanup}) {
		t.Errorf("calls %q, want cleanup, check, cleanup", got)
	}
	if r.DurationMS >= 1000 {
		t.Errorf("duration_ms %d counts the cleanup", r.DurationMS)
	}
}

// On the clusters v3 of B4 and B5 the collection is still the script of
// the kit, never a list of kinds in the CLI: Application and ApplicationSet
// of Argo CD (C3 of B4), the Kyverno policies and the CronJobs of the
// student namespaces (C1 and C2 of B5) are known to lib/cluster-nettoyage.sh
// alone, which skips a kind that the cluster does not serve. The CLI runs
// the script of the kit before and after the run, calls kubectl for none
// of it, and reads CLUSTER_DIRTY from the exit status of the script only.
func TestVerifyClusterV3CleanupFromKit(t *testing.T) {
	for _, c := range []struct{ cluster, conf string }{{"sagnac-b4", confB4}, {"sagnac-b5", confB5}} {
		t.Run(c.cluster, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, ".cache", "sagnac", "clusters", c.cluster, "check.lock"), "", 0o600)
			t.Setenv("HOME", home)
			bin := t.TempDir()
			kubectlLog := filepath.Join(t.TempDir(), "kubectl")
			writeFile(t, filepath.Join(bin, "kubectl"), "#!/bin/sh\necho \"$*\" >>"+kubectlLog+"\nexit 0\n", 0o755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			meta := strings.Replace(clusterMeta, "cluster: sagnac-b1", "cluster: "+c.cluster, 1)
			dir, ws := clusterFixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"})
			writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", c.cluster+".conf"), c.conf, 0o644)
			log := filepath.Join(t.TempDir(), "calls")
			recordingCleanup(t, dir, log, 0)
			lab := mustLoad(t, dir)
			if lab.ClusterContract != 3 {
				t.Fatalf("contract %d, want 3", lab.ClusterContract)
			}
			r, err := Verify(context.Background(), lab, ws, 3, false)
			if err != nil {
				t.Fatal(err)
			}
			if r.ExitCode() != 0 {
				t.Fatalf("checks %v, want pass", codes(r))
			}
			wantCleanup := "cleanup lab=x cluster=" + c.cluster + " check=unset lib=controle.sh pwd=" + resolve(ws)
			if got := logLines(t, log); !slices.Equal(got, []string{wantCleanup, wantCleanup}) {
				t.Errorf("cleanup calls %q, want two runs of the script of the kit", got)
			}
			if calls := logLines(t, kubectlLog); len(calls) > 0 {
				t.Errorf("the CLI called kubectl itself: %q", calls)
			}
		})
	}
}

// Objects left after the opening cleanup: no check runs, each gives
// CLUSTER_DIRTY (platform, exit 2); the closing cleanup still runs.
func TestVerifyClusterDirty(t *testing.T) {
	clusterHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	dir, ws := clusterFixture(t, clusterMeta+check("verify_two", 2, "two.sh", "    codes: [TWO_BAD]\n"),
		map[string]string{"one.sh": ": >" + marker + "\nsagnac_reussite", "two.sh": ": >" + marker + "\nsagnac_reussite"})
	log := filepath.Join(t.TempDir(), "calls")
	recordingCleanup(t, dir, log, 1)
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"error:CLUSTER_DIRTY:platform", "error:CLUSTER_DIRTY:platform"}
	if got := codes(r); !slices.Equal(got, want) || r.ExitCode() != 2 {
		t.Errorf("checks %v exit %d, want %v exit 2", got, r.ExitCode(), want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a check ran on a dirty cluster")
	}
	if n := len(logLines(t, log)); n != 2 {
		t.Errorf("cleanup ran %d times, want 2", n)
	}
}

// A cleanup that outlives its delay counts as a failed cleanup.
func TestVerifyClusterCleanupDelay(t *testing.T) {
	clusterHome(t)
	saved := cleanupDelay
	cleanupDelay = 500 * time.Millisecond
	t.Cleanup(func() { cleanupDelay = saved })
	dir, ws := clusterFixture(t, clusterMeta, map[string]string{"one.sh": "sagnac_reussite"})
	writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "lib", "cluster-nettoyage.sh"), "#!/bin/sh\nsleep 5\n", 0o755)
	start := time.Now()
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(r); !slices.Equal(got, []string{"error:CLUSTER_DIRTY:platform"}) {
		t.Errorf("checks %v, want CLUSTER_DIRTY", got)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Verify took %s: the cleanup was not stopped at its delay", elapsed)
	}
}

// One check at a time per cluster: while another holds check.lock, no
// cleanup and no check runs, each check gives CLUSTER_BUSY (exit 2).
// Without the lock file (cluster never set up), the run goes on.
func TestVerifyClusterBusy(t *testing.T) {
	home := clusterHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	dir, ws := clusterFixture(t, clusterMeta, map[string]string{"one.sh": ": >" + marker + "\nsagnac_reussite"})
	log := filepath.Join(t.TempDir(), "calls")
	recordingCleanup(t, dir, log, 0)
	lock := filepath.Join(home, ".cache", "sagnac", "clusters", "sagnac-b1", "check.lock")
	f, err := os.Open(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	lab := mustLoad(t, dir)
	r, err := Verify(context.Background(), lab, ws, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(r); !slices.Equal(got, []string{"error:CLUSTER_BUSY:platform"}) || r.ExitCode() != 2 {
		t.Errorf("checks %v exit %d, want CLUSTER_BUSY exit 2", got, r.ExitCode())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a check ran while the cluster was busy")
	}
	if got := logLines(t, log); got != nil {
		t.Errorf("cleanup ran while the cluster was busy: %q", got)
	}
	f.Close()

	// Released: the lock is taken, then released at the end of the run.
	r, err = Verify(context.Background(), lab, ws, 3, false)
	if err != nil || r.ExitCode() != 0 {
		t.Fatalf("after release: %v %v", err, codes(r))
	}
	g, err := os.Open(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := syscall.Flock(int(g.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("check.lock still held after the run: %v", err)
	}

	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if r, err := Verify(context.Background(), lab, ws, 3, false); err != nil || r.ExitCode() != 0 {
		t.Errorf("without check.lock: %v %v, want a normal run", err, codes(r))
	}
}

// checks/teardown.sh runs after the last check, also for a lab without a
// cluster and after ORACLE_TIMEOUT, with the environment of the checks.
func TestVerifyTeardown(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	meta := "id: x\nversion: 1\nchecks:\n" + check("verify_slow", 1, "slow.sh", "    timeout_s: 1\n    codes: [ONE_BAD]\n")
	dir, ws := fixture(t, meta, map[string]string{
		"slow.sh":     "echo check >>" + log + "\nsleep 5\nsagnac_reussite",
		"teardown.sh": "echo \"teardown lab=$SAGNAC_LAB check=${SAGNAC_CHECK-unset} seed=$SAGNAC_SEED\" >>" + log,
	}, "ONE_BAD")
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 9, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(r); !slices.Equal(got, []string{"error:ORACLE_TIMEOUT:oracle"}) {
		t.Errorf("checks %v", got)
	}
	if got := logLines(t, log); !slices.Equal(got, []string{"check", "teardown lab=x check=unset seed=9"}) {
		t.Errorf("calls %q", got)
	}
}

// Ctrl-C during the checks: no result, but the closing cleanup and the
// teardown still run.
func TestVerifyClusterInterruptedCleanup(t *testing.T) {
	clusterHome(t)
	started := filepath.Join(t.TempDir(), "started")
	dir, ws := clusterFixture(t, clusterMeta, map[string]string{
		"one.sh": ": >" + started + "\nsleep 30\nsagnac_reussite", "teardown.sh": "echo teardown >>" + started + ".log"})
	log := started + ".log"
	recordingCleanup(t, dir, log, 0)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(started); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := Verify(ctx, mustLoad(t, dir), ws, 3, false); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err %v, want ErrInterrupted", err)
	}
	got := logLines(t, log)
	if len(got) != 3 || !strings.HasPrefix(got[1], "cleanup") || got[2] != "teardown" {
		t.Errorf("calls %q, want cleanup, cleanup, teardown", got)
	}
}

// Variables common to the checks and the cleanup (docs/contrat.md, section
// 3): Git reads the repositories of any owner without their hooks or
// fsmonitor, DOCKER_HOST is kept only for a local engine, KUBECACHEDIR and
// DOCKER_CONFIG only under the TMPDIR of the run.
func TestCommonEnvironment(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for _, c := range []struct {
		name string
		set  map[string]string
		want map[string]string
	}{
		{"local engine", map[string]string{"DOCKER_HOST": "unix:///run/user/1000/docker.sock"},
			map[string]string{"DOCKER_HOST": "unix:///run/user/1000/docker.sock"}},
		{"remote engine", map[string]string{"DOCKER_HOST": "tcp://10.0.0.1:2375"}, nil},
		{"caches under TMPDIR", map[string]string{"KUBECACHEDIR": tmp + "/kube", "DOCKER_CONFIG": tmp + "/docker"},
			map[string]string{"KUBECACHEDIR": tmp + "/kube", "DOCKER_CONFIG": tmp + "/docker"}},
		{"caches elsewhere", map[string]string{"KUBECACHEDIR": "/home/x/.kube/cache", "DOCKER_CONFIG": tmp + "/../docker"}, nil},
		{"relative cache", map[string]string{"KUBECACHEDIR": "kube"}, nil},
		{"prefix is not a parent", map[string]string{"DOCKER_CONFIG": tmp + "x/docker"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []string{"DOCKER_HOST", "KUBECACHEDIR", "DOCKER_CONFIG"} {
				t.Setenv(k, c.set[k])
				if _, ok := c.set[k]; !ok {
					os.Unsetenv(k)
				}
			}
			got := map[string]string{}
			for _, kv := range commonEnv(&Lab{}) {
				k, v, _ := strings.Cut(kv, "=")
				got[k] = v
			}
			want := map[string]string{"GIT_CONFIG_COUNT": "3",
				"GIT_CONFIG_KEY_0": "safe.directory", "GIT_CONFIG_VALUE_0": "*",
				"GIT_CONFIG_KEY_1": "core.fsmonitor", "GIT_CONFIG_VALUE_1": "false",
				"GIT_CONFIG_KEY_2": "core.hooksPath", "GIT_CONFIG_VALUE_2": "/dev/null"}
			for k, v := range c.want {
				want[k] = v
			}
			if !maps.Equal(got, want) {
				t.Errorf("commonEnv = %v, want %v", got, want)
			}
		})
	}
	if env := commonEnv(&Lab{Cluster: "sagnac-b1"}); !slices.Contains(env, "SAGNAC_CLUSTER=sagnac-b1") {
		t.Errorf("commonEnv of a lab with a cluster = %v, no SAGNAC_CLUSTER", env)
	}
}

// fakeDocker puts on PATH a docker that prints out for `docker version`.
func fakeDocker(t *testing.T, out string, rc int) {
	t.Helper()
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf '%s\\n' '"+out+"'\nexit "+strconv.Itoa(rc)+"\n", 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

// For a lab docker: true, the platform names the engine and its server
// version read by docker version, within bounds; null otherwise.
func TestPlatformEngine(t *testing.T) {
	for _, c := range []struct {
		name, out    string
		rc           int
		engine, vers string
	}{
		{"docker", `27.3.1|[{"Name":"Engine","Version":"27.3.1"}]`, 0, "docker", "27.3.1"},
		{"podman", `5.2.0|[{"Name":"Podman Engine","Version":"5.2.0"}]`, 0, "podman", "5.2.0"},
		{"orbstack", `28.5.2|null`, 0, "docker", "28.5.2"},
		{"engine down", `|`, 1, "", ""},
		{"exit code", `27.3.1|[]`, 1, "", ""},
		{"free text", `version 27|[]`, 0, "", ""},
		{"too long", strings.Repeat("1", 33) + `|[]`, 0, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeDocker(t, c.out, c.rc)
			p := currentPlatform(true)
			if deref(p.ContainerEngine) != orNil(c.engine) || deref(p.EngineVersion) != orNil(c.vers) {
				t.Errorf("engine %s %s, want %q %q", deref(p.ContainerEngine), deref(p.EngineVersion), c.engine, c.vers)
			}
		})
	}
	fakeDocker(t, `27.3.1|[]`, 0)
	if p := currentPlatform(false); p.ContainerEngine != nil || p.EngineVersion != nil {
		t.Error("a lab without docker names an engine")
	}
}

func orNil(s string) string {
	if s == "" {
		return "<nil>"
	}
	return s
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The ceiling of a run is 60 s, 240 s with GenAI or a cluster (contract v2
// or v3) and 360 s with GenAI and a cluster; timeout_s of a check stays within
// 60 s without GenAI and 240 s with it (docs/contrat.md, section 3).
func TestRunCeiling(t *testing.T) {
	genAI := strings.Replace(clusterMeta, "version: 1\n", "version: 1\ngenai: true\n", 1)
	plain := "id: x\nversion: 1\nchecks:" + okCheck
	cases := []struct {
		name, meta string
		cluster    bool
		want       int64
	}{
		{"plain", plain, false, 60},
		{"genai", strings.Replace(plain, "version: 1\n", "version: 1\ngenai: true\n", 1), false, 240},
		{"cluster", clusterMeta, true, 240},
		{"cluster v3", clusterMeta, true, 240},
		{"cluster and genai", genAI, true, 360},
		{"cluster v3 and genai", genAI, true, 360},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var dir string
			if c.cluster {
				dir, _ = clusterFixture(t, c.meta, map[string]string{"one.sh": "sagnac_reussite"})
				if strings.Contains(c.name, "v3") {
					writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", "sagnac-b1.conf"),
						"API_PORT=6560\nCONTRAT=3\n", 0o644)
				}
			} else {
				dir, _ = fixture(t, c.meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
			}
			if got := runCeilingSeconds(mustLoad(t, dir)); got != c.want {
				t.Errorf("ceiling %d s, want %d s", got, c.want)
			}
		})
	}
	for _, c := range []struct {
		timeout string
		ok      bool
	}{{"240", true}, {"241", false}, {"360", false}} {
		meta := strings.Replace(genAI, "    preconditions: [CLUSTER_MISSING]\n",
			"    preconditions: [CLUSTER_MISSING]\n    timeout_s: "+c.timeout+"\n", 1)
		dir, _ := clusterFixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"})
		_, err := LoadLab(dir)
		var kit *KitError
		if c.ok != (err == nil) || (!c.ok && !errors.As(err, &kit)) {
			t.Errorf("timeout_s %s with a cluster and GenAI: error %v, accepted %v", c.timeout, err, c.ok)
		}
	}
	// Without GenAI, a check of a lab with a cluster stays within 60 s, even
	// though the run may last 240 s (reference executor: PLAFOND_S[genai]).
	for _, c := range []struct {
		timeout string
		ok      bool
	}{{"60", true}, {"61", false}, {"240", false}} {
		meta := strings.Replace(clusterMeta, "    preconditions: [CLUSTER_MISSING]\n",
			"    preconditions: [CLUSTER_MISSING]\n    timeout_s: "+c.timeout+"\n", 1)
		dir, _ := clusterFixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"})
		_, err := LoadLab(dir)
		var kit *KitError
		if c.ok != (err == nil) || (!c.ok && !errors.As(err, &kit)) {
			t.Errorf("timeout_s %s with a cluster: error %v, accepted %v", c.timeout, err, c.ok)
		}
	}
}
