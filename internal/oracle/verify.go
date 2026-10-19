package oracle

import (
	"context"
	"os"
	"time"
)

const sessionLocal = "local"

type networkCutError struct{}

func (networkCutError) Error() string { return "ORACLE_NETWORK_CUT_UNAVAILABLE" }

// ErrNetworkCutUnavailable: noNetwork was asked and this machine has no way
// to cut the network. Verify never runs the checks with the network open in
// that case; the command decides what to do (warning, run without the cut).
var ErrNetworkCutUnavailable error = networkCutError{}

type interruptedError struct{}

func (interruptedError) Error() string { return "ORACLE_INTERRUPTED" }

// ErrInterrupted: the context was cancelled (Ctrl-C) during the run. The
// check that was running is killed with its process group; no result is
// returned, so nothing can be shown as a verdict or sent.
var ErrInterrupted error = interruptedError{}

// Verify runs every check of the lab once against workspace, with no memory
// of a previous run (docs/contrat.md, section 3): dependencies not
// passed give skip, the run ceiling gives ORACLE_TOTAL_TIMEOUT to the
// remaining checks, and the exit code follows the priority 3, 2, 1, 0.
// noNetwork cuts outgoing network (sandbox-exec, unshare -rn); without a
// method, Verify returns ErrNetworkCutUnavailable and runs nothing. A
// cancelled ctx (Ctrl-C) kills the running check with its process group and
// gives ErrInterrupted, never a result. A workspace that is not a
// directory gives a *UsageError.
func Verify(ctx context.Context, lab *Lab, workspace string, seed int64, noNetwork bool) (*Result, error) {
	workspace = resolve(workspace)
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return nil, &UsageError{Reason: "workspace not found", Path: workspace}
	}
	var prefix []string
	if noNetwork {
		if prefix, _ = networkWrapper(); prefix == nil {
			return nil, ErrNetworkCutUnavailable
		}
	}
	startClock := time.Now().UTC().Truncate(time.Second)
	start := time.Now()
	ceiling := time.Duration(capSeconds(lab.GenAI)) * time.Second
	results := make([]CheckResult, 0, len(lab.Checks))
	byID := map[string]CheckResult{}
	for i := range lab.Checks {
		c := &lab.Checks[i]
		var blocker *string
		for _, d := range c.DependsOn {
			if byID[d].Status != statusPass {
				blocker = ptr(d)
				break
			}
		}
		if ctx.Err() != nil {
			return nil, ErrInterrupted
		}
		left := ceiling - time.Since(start)
		var r CheckResult
		switch {
		case blocker != nil:
			r = outcome(c, statusSkip, nil, nil, nil, nil, blocker, 0)
		case left <= 0:
			r = oracleError(c, codeTotalTimeout, 0)
		default:
			var interrupted bool
			r, interrupted = runCheck(ctx, lab, c, workspace, seed, min(time.Duration(c.DelaySeconds)*time.Second, left), prefix)
			if interrupted {
				return nil, ErrInterrupted
			}
		}
		results = append(results, r)
		byID[c.ID] = r
	}

	status, exit := statusPass, ExitPass
	oracle, platform, failed := false, false, false
	for _, r := range results {
		switch {
		case r.Status == statusError && *r.Origin == originOracle:
			oracle = true
		case r.Status == statusError && *r.Origin == originPlatform:
			platform = true
		case r.Status == statusFail:
			failed = true
		}
	}
	switch {
	case oracle:
		status, exit = statusError, ExitOracle
	case platform:
		status, exit = statusError, ExitPlatform
	case failed:
		status, exit = statusFail, ExitFail
	}
	digest, err := ChecksDigest(lab.Dir)
	if err != nil {
		return nil, &KitError{Where: "checks/", Reason: "unreadable"}
	}
	return &Result{
		Schema:       Schema,
		CLI:          CLIName,
		Lab:          LabRef{ID: lab.ID, Version: lab.Version},
		ChecksSHA256: digest,
		Session:      sessionLocal,
		Seed:         seed,
		Start:        startClock.Format("2006-01-02T15:04:05Z"),
		DurationMS:   time.Since(start).Milliseconds(),
		Status:       status,
		Exit:         exit,
		Checks:       results,
		Platform:     currentPlatform(),
	}, nil
}
