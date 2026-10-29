package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

// withServices makes the lab depend on svc and shortens the wait.
func withServices(t *testing.T, timeout time.Duration, svc ...oracle.Service) {
	t.Helper()
	oldS, oldT, oldI := labServices, readyTimeout, readyInterval
	labServices = func(*oracle.Lab) []oracle.Service { return svc }
	readyTimeout, readyInterval = timeout, 2*time.Millisecond
	t.Cleanup(func() { labServices, readyTimeout, readyInterval = oldS, oldT, oldI })
}

// The services are waited for before the session is asked and before the
// first check, and the time they take is not in the duration of the checks.
func TestServicesAreWaitedForBeforeTheChecks(t *testing.T) {
	c := newCheckRig(t, "")
	var probes atomic.Int32
	sessionsSeen := -1
	withServices(t, 5*time.Second, oracle.Service{Name: "api", Ready: func(context.Context) bool {
		n := probes.Add(1)
		sessionsSeen = max(sessionsSeen, c.plat.sessionCount())
		if n < 3 {
			return false
		}
		time.Sleep(300 * time.Millisecond) // a slow probe: the checks must not pay for it
		return true
	}})
	var probesAtCheck int32
	old := verify
	t.Cleanup(func() { verify = old })
	verify = func(ctx context.Context, lab *oracle.Lab, ws string, seed int64, noNetwork bool) (*oracle.Result, error) {
		probesAtCheck = probes.Load()
		return old(ctx, lab, ws, seed, noNetwork)
	}
	code, stdout, stderr := c.run(t, "check", "--json")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, %s", code, stderr)
	}
	if probesAtCheck != 3 {
		t.Errorf("the checks started after %d probes, want 3", probesAtCheck)
	}
	if sessionsSeen != 0 {
		t.Errorf("a session was asked for during the wait (%d): it could expire before the checks", sessionsSeen)
	}
	if c.plat.sessionCount() != 1 || len(c.plat.sent()) != 1 {
		t.Errorf("sessions %d, attempts %d", c.plat.sessionCount(), len(c.plat.sent()))
	}
	var r struct {
		DurationMS int64 `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not the JSON result alone: %v\n%s", err, stdout)
	}
	if r.DurationMS >= 300 {
		t.Errorf("duration_ms = %d: the wait is counted in the checks", r.DurationMS)
	}
	if !strings.Contains(stderr, i18n.T("check.waiting", "api", wholeSeconds(readyTimeout))) {
		t.Errorf("the wait is not announced on stderr: %q", stderr)
	}
	if strings.Contains(stdout, i18n.T("check.waiting", "api", wholeSeconds(readyTimeout))) {
		t.Error("the wait must not appear on stdout with --json")
	}
}

// After the limit the checks run anyway: they report the missing service
// themselves (a platform precondition), exactly as without a wait.
func TestServicesGiveUpAtTheLimit(t *testing.T) {
	c := newCheckRig(t, "")
	withServices(t, 40*time.Millisecond, oracle.Service{Name: "api", Ready: func(context.Context) bool { return false }})
	code, _, stderr := c.run(t, "check")
	if code != oracle.ExitFail {
		t.Errorf("code %d: the verdict is the checks', not the wait's", code)
	}
	if !strings.Contains(stderr, i18n.T("check.ready_timeout", "api", wholeSeconds(40*time.Millisecond))) {
		t.Errorf("stderr %q", stderr)
	}
	if len(c.plat.sent()) != 1 {
		t.Error("the result of the checks must still be sent")
	}
}

func TestNoServicesNoWait(t *testing.T) {
	c := newCheckRig(t, "")
	start := time.Now()
	code, stdout, stderr := c.run(t, "check")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, %s", code, stderr)
	}
	if time.Since(start) > 5*time.Second || strings.Contains(stderr+stdout, "En attente du démarrage") {
		t.Errorf("a lab without service waited: %v\n%s", time.Since(start), stderr)
	}
}

func TestInterruptedDuringTheWait(t *testing.T) {
	c := newCheckRig(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	withServices(t, 10*time.Second, oracle.Service{Name: "api", Ready: func(context.Context) bool { cancel(); return false }})
	called := false
	old := verify
	t.Cleanup(func() { verify = old })
	verify = func(context.Context, *oracle.Lab, string, int64, bool) (*oracle.Result, error) {
		called = true
		return nil, oracle.ErrInterrupted
	}
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	code := runContext(ctx, []string{"check"}, noEnv, &out, &errOut)
	if code != exitInterrupted || called {
		t.Errorf("code %d, verify called: %v", code, called)
	}
	if c.plat.sessionCount() != 0 || len(c.plat.sent()) != 0 || len(c.pending(t)) != 0 {
		t.Error("an interrupted wait must send and queue nothing")
	}
	if !strings.Contains(errOut.String(), i18n.T("check.interrupted")) {
		t.Errorf("stderr %q", errOut.String())
	}
}

// A lab that declares a cluster waits for it, in the Sagnac cache, once the
// cluster exists; a cluster never created is not waited for.
func TestDefaultServicesAreTheCluster(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(t.TempDir(), "sagnac")
	t.Setenv("SAGNAC_CACHE", cache)
	lab := &oracle.Lab{Dir: dir}
	if got := defaultServices(lab); got != nil {
		t.Errorf("a lab without cluster has no service: %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte("id: m2-l2\nversion: 1\ncluster: sagnac-b1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := defaultServices(lab)
	if len(got) != 1 || got[0].Name != "sagnac-b1" || got[0].Present() {
		t.Fatalf("services = %+v", got)
	}
	state := filepath.Join(cache, "clusters", "sagnac-b1")
	for name, mode := range map[string]os.FileMode{"kubeconfig": 0o600, "kubectl": 0o755} {
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, name), []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if !got[0].Present() || !got[0].Ready(context.Background()) {
		t.Error("an existing cluster whose API answers is present and ready")
	}
}

func TestReadyRuleIsTheFrameworks(t *testing.T) {
	// 180 s and 2 s: the bounds of bin/sagnac-cluster (k3d --wait --timeout
	// 180s, api_prete every 2 s).
	if oracle.ReadyTimeout != 180*time.Second || oracle.ReadyInterval != 2*time.Second {
		t.Errorf("ready rule = %v every %v", oracle.ReadyTimeout, oracle.ReadyInterval)
	}
}

// A lab of the cluster contract v3 waits for its cluster like a lab v2: the
// wait reads the cluster of meta.yaml, whatever the version in its .conf,
// and ends before the checks, hence before their ceiling of 240 s starts.
func TestClusterV3IsWaitedFor(t *testing.T) {
	c := newCheckRig(t, clusterV3Meta)
	cadre := filepath.Join(filepath.Dir(c.labDir), "_cadre")
	cache := filepath.Join(t.TempDir(), "sagnac")
	t.Setenv("SAGNAC_CACHE", cache)
	state := filepath.Join(cache, "clusters", "sagnac-b3")
	probes := filepath.Join(state, "probes")
	// The API answers /readyz from the third probe on.
	kubectl := "#!/bin/sh\necho probe >> " + probes + "\n[ \"$(wc -l < " + probes + ")\" -ge 3 ]\n"
	for name, f := range map[string]struct {
		body string
		mode os.FileMode
	}{
		filepath.Join(cadre, "clusters", "sagnac-b3.conf"):                       {"API_PORT=6560\nCONTRAT=3\n", 0o644},
		filepath.Join(cadre, "lib", "cluster-nettoyage.sh"):                      {"#!/bin/sh\nexit 0\n", 0o755},
		filepath.Join(c.labDir, "checks", "messages", "fr", "CLUSTER_DIRTY.txt"): {"Sale.\n", 0o644},
		filepath.Join(c.labDir, "checks", "messages", "fr", "CLUSTER_BUSY.txt"):  {"Occupé.\n", 0o644},
		filepath.Join(state, "kubeconfig"):                                       {"", 0o600},
		filepath.Join(state, "kubectl"):                                          {kubectl, 0o755},
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
	}
	oldT, oldI := readyTimeout, readyInterval
	readyTimeout, readyInterval = 5*time.Second, 2*time.Millisecond
	t.Cleanup(func() { readyTimeout, readyInterval = oldT, oldI })
	var probesAtCheck string
	var contract int
	old := verify
	t.Cleanup(func() { verify = old })
	verify = func(ctx context.Context, lab *oracle.Lab, ws string, seed int64, noNetwork bool) (*oracle.Result, error) {
		raw, _ := os.ReadFile(probes)
		probesAtCheck, contract = string(raw), lab.ClusterContract
		return old(ctx, lab, ws, seed, noNetwork)
	}
	c.pass(t)
	code, _, stderr := c.run(t, "check")
	if code != oracle.ExitPass {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if contract != 3 || strings.Count(probesAtCheck, "probe") != 3 {
		t.Errorf("contract %d, probes before the checks %q; want 3 and three probes", contract, probesAtCheck)
	}
	if !strings.Contains(stderr, i18n.T("check.waiting", "sagnac-b3", wholeSeconds(readyTimeout))) {
		t.Errorf("the wait for sagnac-b3 is not announced: %q", stderr)
	}
}
