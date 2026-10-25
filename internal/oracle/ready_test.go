package oracle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// after returns a service that becomes ready on its n-th probe.
func after(name string, n int32) (Service, *atomic.Int32) {
	var calls atomic.Int32
	return Service{Name: name, Ready: func(context.Context) bool { return calls.Add(1) >= n }}, &calls
}

func TestWaitReadyReturnsAsSoonAsEverythingAnswers(t *testing.T) {
	a, ca := after("a", 1)
	b, cb := after("b", 3)
	var told [][]string
	res, err := WaitReady(context.Background(), []Service{a, b}, time.Second, time.Millisecond,
		func(waiting []string) { told = append(told, append([]string{}, waiting...)) })
	if err != nil || !res.Ready {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if ca.Load() != 1 {
		t.Errorf("a is ready and stays so, probed %d times", ca.Load())
	}
	if cb.Load() != 3 {
		t.Errorf("b probed %d times, want 3", cb.Load())
	}
	// Told once, with the services still down at that moment.
	if len(told) != 1 || len(told[0]) != 1 || told[0][0] != "b" {
		t.Errorf("progress = %v", told)
	}
}

func TestWaitReadyNothingToWaitFor(t *testing.T) {
	told := 0
	res, err := WaitReady(context.Background(), nil, time.Second, time.Millisecond, func([]string) { told++ })
	if err != nil || !res.Ready || told != 0 || res.Waited != 0 {
		t.Errorf("res = %+v, err = %v, told = %d", res, err, told)
	}
}

func TestWaitReadyAlreadyReadyIsSilent(t *testing.T) {
	a, _ := after("a", 1)
	told := 0
	res, err := WaitReady(context.Background(), []Service{a}, time.Second, time.Millisecond, func([]string) { told++ })
	if err != nil || !res.Ready || told != 0 {
		t.Errorf("res = %+v, err = %v, told = %d", res, err, told)
	}
}

func TestWaitReadyGivesUpAtTheLimit(t *testing.T) {
	never := Service{Name: "api", Ready: func(context.Context) bool { return false }}
	start := time.Now()
	res, err := WaitReady(context.Background(), []Service{never}, 60*time.Millisecond, 5*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("err = %v: giving up is not an error, the checks run and say what is missing", err)
	}
	if res.Ready || len(res.Down) != 1 || res.Down[0] != "api" {
		t.Errorf("res = %+v", res)
	}
	if took := time.Since(start); took < 55*time.Millisecond || took > 2*time.Second {
		t.Errorf("waited %v for a limit of 60ms", took)
	}
}

func TestWaitReadyCutsAProbeAtTheLimit(t *testing.T) {
	// A probe that hangs until its context ends must not hold the check back.
	hang := Service{Name: "api", Ready: func(ctx context.Context) bool { <-ctx.Done(); return false }}
	start := time.Now()
	res, err := WaitReady(context.Background(), []Service{hang}, 50*time.Millisecond, 5*time.Millisecond, nil)
	if err != nil || res.Ready {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("a hanging probe was not cut at the limit")
	}
}

func TestWaitReadyInterrupted(t *testing.T) {
	never := Service{Name: "api", Ready: func(context.Context) bool { return false }}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := WaitReady(ctx, []Service{never}, 10*time.Second, 5*time.Millisecond, nil)
	if !errors.Is(err, ErrInterrupted) {
		t.Errorf("err = %v, want ErrInterrupted", err)
	}
}

func TestAbsentServiceIsNotWaitedFor(t *testing.T) {
	// A cluster that was never created cannot become ready by waiting: the
	// check says CLUSTER_MISSING at once.
	probed := false
	absent := Service{Name: "api", Present: func() bool { return false },
		Ready: func(context.Context) bool { probed = true; return false }}
	start := time.Now()
	res, err := WaitReady(context.Background(), []Service{absent}, time.Second, 5*time.Millisecond, nil)
	if err != nil || !res.Ready || probed || time.Since(start) > 500*time.Millisecond {
		t.Errorf("res = %+v, err = %v, probed = %v", res, err, probed)
	}
}

// fakeKubectl answers /readyz from the file "up" next to it.
const fakeKubectl = `#!/bin/sh
echo "$@" >> "$(dirname "$0")/args"
[ -f "$(dirname "$0")/up" ]
`

func TestClusterReadyAsksTheControlIdentity(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "clusters", "sagnac-b1")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := ClusterReady(root, "sagnac-b1")
	if svc.Present() {
		t.Error("a cluster without kubeconfig is absent")
	}
	if err := os.WriteFile(filepath.Join(state, "kubeconfig"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "kubectl"), []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	if !svc.Present() {
		t.Error("kubeconfig and kubectl are there: present")
	}
	if svc.Ready(context.Background()) {
		t.Error("API down: not ready")
	}
	if err := os.WriteFile(filepath.Join(state, "up"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !svc.Ready(context.Background()) {
		t.Error("API up: ready")
	}
	raw, _ := os.ReadFile(filepath.Join(state, "args"))
	want := "--kubeconfig " + filepath.Join(state, "kubeconfig") + " --request-timeout=5s get --raw /readyz\n"
	if string(raw) != want+want {
		t.Errorf("kubectl args = %q, want twice %q", raw, want)
	}
}

func TestClusterReadyRefusesAHostileName(t *testing.T) {
	root := t.TempDir()
	svc := ClusterReady(root, "../../etc")
	if svc.Present() || svc.Ready(context.Background()) {
		t.Error("a name outside [a-z0-9-] is never probed")
	}
}
