package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/giis-io/sagnac-cli/internal/cleanup"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

// clusterScript is where the lab framework keeps the script that creates and
// deletes the cluster of a lab, from the folder that holds the kit.
var clusterScript = filepath.Join("_cadre", "bin", "sagnac-cluster")

type cleanOptions struct {
	all    bool
	dryRun bool
}

// parseClean reads "[--all] [--dry-run]" in any order.
func parseClean(args []string, stderr io.Writer) (cleanOptions, bool) {
	var o cleanOptions
	for _, a := range args {
		switch {
		case a == "--all":
			o.all = true
		case a == "--dry-run":
			o.dryRun = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
			return o, false
		default:
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, "clean"))
			return o, false
		}
	}
	return o, true
}

// runClean deletes the cluster of the current lab (every cluster with --all)
// and the half-written files of the Sagnac cache, and keeps the models, the
// tools and the base images. The cluster goes through the script of the lab
// framework (the same as "make cluster-delete"), so that both leave the same
// state. Nothing outside the Sagnac cache and the lab folders is touched, and
// the workspace of the student never is.
func runClean(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, ok := parseClean(args, stderr)
	if !ok {
		return exitCodeOf(errUsage)
	}
	root, err := cleanup.Root(os.Getenv)
	if err != nil {
		return reportCleanRoot(stderr, err)
	}
	labDirs, names, scripts, code := cleanTargets(o, stderr)
	if code >= 0 {
		return code
	}
	plan, err := cleanup.Build(root, names, o.all)
	if err != nil {
		return reportCleanRoot(stderr, err)
	}
	var labTemp []string
	for _, dir := range labDirs {
		found, err := cleanup.LabTemporary(dir)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("clean.failed", dir))
			return exitCodeOf(errStorage)
		}
		labTemp = append(labTemp, found...)
	}
	for _, p := range plan.Refused {
		fmt.Fprintln(stderr, i18n.T("clean.ignored_link", p))
	}
	if len(plan.Clusters) == 0 && len(plan.Temporary) == 0 && len(labTemp) == 0 {
		fmt.Fprintln(stdout, i18n.T("clean.nothing"))
		fmt.Fprintln(stdout, i18n.T("clean.kept", root))
		return exitOK
	}
	if o.dryRun {
		fmt.Fprintln(stdout, i18n.T("clean.dry_run"))
		for _, c := range plan.Clusters {
			fmt.Fprintln(stdout, i18n.T("clean.would_cluster", c.Name, c.State))
		}
		for _, p := range append(append([]string{}, plan.Temporary...), labTemp...) {
			fmt.Fprintln(stdout, i18n.T("clean.would_temporary", p))
		}
		fmt.Fprintln(stdout, i18n.T("clean.kept", root))
		return exitOK
	}

	result := exitOK
	for _, c := range plan.Clusters {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, i18n.T("error.interrupted"))
			return exitCodeOf(ctx.Err())
		}
		script := firstScript(scripts)
		if script == "" {
			fmt.Fprintln(stderr, i18n.T("clean.no_script", c.Name))
			result = keepWorst(result, exitCodeOf(errRefused))
			continue
		}
		if !o.all {
			fmt.Fprintln(stdout, i18n.T("clean.shared", c.Name))
		}
		var detail bytes.Buffer
		cmd := exec.CommandContext(ctx, "sh", script, "delete", c.Name)
		cmd.Env = append(os.Environ(), "SAGNAC_CACHE="+root)
		cmd.Stderr = &detail
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(stderr, i18n.T("error.interrupted"))
				return exitCodeOf(ctx.Err())
			}
			fmt.Fprintln(stderr, i18n.T("clean.cluster_failed", c.Name))
			if text := strings.TrimSpace(detail.String()); text != "" {
				fmt.Fprintln(stderr, messageIndent+strings.ReplaceAll(text, "\n", "\n"+messageIndent))
			}
			result = keepWorst(result, exitCodeOf(errUnreachable))
			continue
		}
		fmt.Fprintln(stdout, i18n.T("clean.cluster_done", c.Name))
	}
	if err := cleanup.RemoveTemporary(root, plan.Temporary); err != nil {
		return reportCleanRoot(stderr, err)
	}
	for _, dir := range labDirs {
		mine := labTempOf(dir, labTemp)
		if err := cleanup.RemoveTemporary(dir, mine); err != nil {
			return reportCleanRoot(stderr, err)
		}
	}
	for _, p := range append(append([]string{}, plan.Temporary...), labTemp...) {
		fmt.Fprintln(stdout, i18n.T("clean.temporary_done", p))
	}
	fmt.Fprintln(stdout, i18n.T("clean.kept", root))
	return result
}

// keepWorst keeps the first failure code: a later one does not hide it.
func keepWorst(current, next int) int {
	if current != exitOK {
		return current
	}
	return next
}

func labTempOf(dir string, all []string) []string {
	var mine []string
	for _, p := range all {
		if filepath.Dir(p) == dir {
			mine = append(mine, p)
		}
	}
	return mine
}

func reportCleanRoot(stderr io.Writer, err error) int {
	switch {
	case errors.Is(err, cleanup.ErrNoCache):
		fmt.Fprintln(stderr, i18n.T("clean.no_cache"))
		return exitCodeOf(errRefused)
	case errors.Is(err, cleanup.ErrUnsafe):
		fmt.Fprintln(stderr, i18n.T("clean.unsafe"))
		return exitCodeOf(errRefused)
	}
	fmt.Fprintln(stderr, i18n.T("clean.failed", ""))
	return exitCodeOf(errStorage)
}

// cleanTargets says where to look: the lab folders whose leftovers are
// removed, the clusters the lab names, and the places where the framework
// script may be. The returned code is -1 when the command goes on.
func cleanTargets(o cleanOptions, stderr io.Writer) (labDirs, names, scripts []string, code int) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("check.no_lab", labNotFoundInDirectory))
		return nil, nil, nil, exitCodeOf(errUsage)
	}
	if o.all {
		labDirs = append(labDirs, childLabs(cwd)...)
		return labDirs, nil, scriptsFrom(cwd, labDirs), -1
	}
	dir, ok := findLab("", stderr)
	if !ok {
		return nil, nil, nil, exitCodeOf(errUsage)
	}
	name, err := oracle.DeclaredCluster(dir)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("check.invalid_kit", "meta.yaml"))
		return nil, nil, nil, exitCodeOf(err)
	}
	if name != "" {
		names = []string{name}
	}
	return []string{dir}, names, scriptsFrom(cwd, []string{dir}), -1
}

// childLabs lists the folder itself and its direct subfolders that hold a
// meta.yaml; links are not followed.
func childLabs(cwd string) []string {
	var labs []string
	isLab := func(d string) bool {
		info, err := os.Lstat(filepath.Join(d, "meta.yaml"))
		return err == nil && info.Mode().IsRegular()
	}
	if isLab(cwd) {
		labs = append(labs, cwd)
	}
	entries, _ := os.ReadDir(cwd)
	for _, e := range entries {
		if e.IsDir() && isLab(filepath.Join(cwd, e.Name())) {
			labs = append(labs, filepath.Join(cwd, e.Name()))
		}
	}
	return labs
}

// scriptsFrom lists where the framework script may be: next to the labs (the
// kit holds _cadre/ beside the lab folder, or two levels up for a module),
// then in the folders above the current one and in its direct subfolders.
func scriptsFrom(cwd string, labs []string) []string {
	var found []string
	for _, lab := range labs {
		found = append(found,
			filepath.Join(lab, "..", clusterScript), filepath.Join(lab, "..", "..", clusterScript))
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		found = append(found, filepath.Join(dir, clusterScript))
		if filepath.Dir(dir) == dir {
			break
		}
	}
	entries, _ := os.ReadDir(cwd)
	for _, e := range entries {
		if e.IsDir() {
			found = append(found, filepath.Join(cwd, e.Name(), clusterScript))
		}
	}
	return found
}

func firstScript(candidates []string) string {
	for _, c := range candidates {
		if info, err := os.Lstat(c); err == nil && info.Mode().IsRegular() {
			return filepath.Clean(c)
		}
	}
	return ""
}
