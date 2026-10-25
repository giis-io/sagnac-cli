package oracle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// The wait for the services of a lab comes before the checks and outside
// their ceiling (docs/contrat.md, section 3): the ceiling of 60, 240 or 360 s
// only counts the checks themselves, so a cluster that is still starting does
// not eat the time of the checks.
//
// The rule is the one of the lab framework (bin/sagnac-cluster): a service is
// ready when its probe answers; probes are repeated every 2 s, one at a time
// as long as they fail, and a service that answered is not probed again. The
// framework waits at most 180 s for its components (k3d --wait --timeout 180s,
// rollout status --timeout=180s) and the CLI takes the same bound.
const (
	// ReadyTimeout is the longest wait for the services of a lab.
	ReadyTimeout = 180 * time.Second
	// ReadyInterval separates two probes (api_prete of bin/sagnac-cluster).
	ReadyInterval = 2 * time.Second

	probeTimeout = 5 * time.Second // --request-timeout=5s of api_prete
)

// Service is something a lab needs running before its checks start.
type Service struct {
	Name string
	// Present says whether the service exists at all. A service that is not
	// present (a cluster never created) cannot become ready by waiting: it is
	// not waited for and the checks report it as missing. Nil: present.
	Present func() bool
	// Ready probes the service once; it must give up when ctx ends.
	Ready func(ctx context.Context) bool
}

// ReadyResult is the outcome of WaitReady.
type ReadyResult struct {
	Ready  bool
	Waited time.Duration
	Down   []string // names of the services that never answered
}

// WaitReady waits until every present service answers, for timeout at most.
// progress, when not nil, is told once, with the services that do not answer
// yet, the first time a wait is needed. Giving up is not an error: the checks
// run anyway and report what is missing with their own preconditions, exactly
// as when there is no wait. ErrInterrupted when ctx is cancelled.
func WaitReady(ctx context.Context, services []Service, timeout, interval time.Duration, progress func(waiting []string)) (ReadyResult, error) {
	start := time.Now()
	var pending []Service
	for _, s := range services {
		if s.Present == nil || s.Present() {
			pending = append(pending, s)
		}
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	told, waited := false, false
	for {
		if ctx.Err() != nil {
			return ReadyResult{}, ErrInterrupted
		}
		var down []Service
		for _, s := range pending {
			if !s.Ready(wctx) {
				down = append(down, s)
			}
		}
		pending = down
		if len(pending) == 0 {
			if !waited {
				return ReadyResult{Ready: true}, nil // nothing was waited for
			}
			return ReadyResult{Ready: true, Waited: time.Since(start)}, nil
		}
		if !told && progress != nil && wctx.Err() == nil {
			progress(names(pending))
			told = true
		}
		waited = true
		select {
		case <-wctx.Done():
			if ctx.Err() != nil {
				return ReadyResult{}, ErrInterrupted
			}
			return ReadyResult{Waited: time.Since(start), Down: names(pending)}, nil
		case <-time.After(interval):
		}
	}
}

func names(services []Service) []string {
	out := make([]string, len(services))
	for i, s := range services {
		out[i] = s.Name
	}
	return out
}

var reClusterName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ClusterReady is the cluster of a lab, in the Sagnac cache at cacheRoot: ready
// when its API answers /readyz to the "control" identity, the one the checks
// use (never the administrator's kubeconfig), like `sagnac-cluster status`.
func ClusterReady(cacheRoot, name string) Service {
	svc := Service{Name: name, Present: func() bool { return false }, Ready: func(context.Context) bool { return false }}
	if !reClusterName.MatchString(name) {
		return svc
	}
	state := filepath.Join(cacheRoot, "clusters", name)
	kubectl := filepath.Join(state, "kubectl")
	kubeconfig := filepath.Join(state, "kubeconfig")
	svc.Present = func() bool { return isRegular(kubectl) && executable(kubectl) && isRegular(kubeconfig) }
	svc.Ready = func(ctx context.Context) bool {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		cmd := exec.CommandContext(pctx, kubectl, "--kubeconfig", kubeconfig,
			"--request-timeout=5s", "get", "--raw", "/readyz")
		cmd.Env = append(os.Environ(), "KUBECONFIG=")
		return cmd.Run() == nil
	}
	return svc
}
