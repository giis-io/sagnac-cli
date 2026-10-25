package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/cleanup"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

// Replaced by the tests: the services of a lab and the bounds of the wait.
var (
	labServices   = defaultServices
	readyTimeout  = oracle.ReadyTimeout
	readyInterval = oracle.ReadyInterval
)

// defaultServices are the services a lab needs before its checks: its
// cluster, when meta.yaml declares one (docs/contrat.md, section 3).
func defaultServices(lab *oracle.Lab) []oracle.Service {
	name, err := oracle.DeclaredCluster(lab.Dir)
	if err != nil || name == "" {
		return nil
	}
	root, err := cleanup.Root(os.Getenv)
	if err != nil {
		return nil
	}
	return []oracle.Service{oracle.ClusterReady(root, name)}
}

// waitForServices waits for the services of the lab, up to readyTimeout, then
// lets the checks start whatever the outcome: a service still down is
// reported by the checks themselves. stop is true when the person interrupted
// the wait; code is then the exit code.
func waitForServices(ctx context.Context, lab *oracle.Lab, stderr io.Writer) (code int, stop bool) {
	services := labServices(lab)
	if len(services) == 0 {
		return 0, false
	}
	res, err := oracle.WaitReady(ctx, services, readyTimeout, readyInterval, func(waiting []string) {
		fmt.Fprintln(stderr, i18n.T("check.waiting", strings.Join(waiting, ", "), wholeSeconds(readyTimeout)))
	})
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("check.interrupted"))
		return exitCodeOf(err), true
	}
	switch {
	case !res.Ready:
		fmt.Fprintln(stderr, i18n.T("check.ready_timeout", strings.Join(res.Down, ", "), wholeSeconds(readyTimeout)))
	case res.Waited > 0:
		fmt.Fprintln(stderr, i18n.T("check.ready_after", wholeSeconds(res.Waited)))
	}
	return 0, false
}

// wholeSeconds rounds a duration up to whole seconds.
func wholeSeconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }
