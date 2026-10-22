package oracle

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Labs with a local Kubernetes cluster (docs/contrat.md, section 7). The
// cluster is created by make setup of the kit (_cadre/bin/sagnac-cluster),
// never by check; check only talks to its API with the "control" identity.
const (
	// codeClusterBusy: another check holds the cluster (lock check.lock).
	codeClusterBusy = "CLUSTER_BUSY"
	// codeClusterDirty: objects left in the namespace after the opening cleanup.
	codeClusterDirty = "CLUSTER_DIRTY"
)

var (
	reCluster  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	reConfKey  = regexp.MustCompile(`^[A-Z_]+$`)
	reConfPort = regexp.MustCompile(`^[0-9]+$`)
	// clusterTools are the tools a lab with a cluster declares in tools
	// (bin/sagnac-cluster reads them from the Sagnac cache).
	clusterTools = []string{"k3d", "kubectl", "k3s", "k3s-airgap", "k3d-tools"}
)

// cadreDir is the _cadre directory next to the lab.
func (l *Lab) cadreDir() string { return filepath.Dir(filepath.Dir(l.libPath)) }

// loadCluster reads the optional key cluster of meta.yaml: the name of a
// _cadre/clusters/<name>.conf of the kit, with docker: true and the tools of
// a cluster in tools. The preconditions are checked once they are read.
func (l *Lab) loadCluster(meta map[string]any) error {
	raw, present := meta["cluster"]
	if !present || raw == nil {
		return nil
	}
	name, ok := raw.(string)
	if !ok {
		return kitErr("meta.yaml", "cluster is the name of a cluster of the cadre")
	}
	lib, err := findLib(l.Dir)
	if err != nil {
		return err
	}
	port, err := clusterPort(filepath.Dir(filepath.Dir(lib)), name)
	if err != nil {
		return err
	}
	if !l.Docker {
		return kitErr("meta.yaml", "a lab with a cluster declares docker: true")
	}
	var missing []string
	for _, tool := range clusterTools {
		if !declaresTool(meta["tools"], tool) {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return kitErr("meta.yaml", "a lab with a cluster declares in tools: "+strings.Join(missing, ", "))
	}
	l.Cluster, l.APIPort = name, port
	return nil
}

// declaresTool is Python's `tool in (tools or [])`.
func declaresTool(tools any, tool string) bool {
	switch x := tools.(type) {
	case []any:
		return slices.Contains(x, any(tool))
	case string:
		return strings.Contains(x, tool)
	case map[string]any:
		_, ok := x[tool]
		return ok
	}
	return false
}

// clusterPort reads API_PORT in <cadre>/clusters/<name>.conf: lines
// KEY=value, the first one of a key wins, same reading as bin/sagnac-cluster.
func clusterPort(cadre, name string) (int, error) {
	conf := filepath.Join(cadre, "clusters", name+".conf")
	if !reCluster.MatchString(name) || !isRegular(conf) {
		return 0, kitErr("meta.yaml", "cluster "+strconv.Quote(name)+" unknown to the cadre (clusters/<name>.conf)")
	}
	raw, err := os.ReadFile(conf)
	if err != nil {
		return 0, kitErr("clusters/"+name+".conf", "unreadable")
	}
	values := map[string]string{}
	for _, line := range pySplitLines(string(raw)) {
		key, value, found := strings.Cut(line, "=")
		if _, seen := values[key]; found && reConfKey.MatchString(key) && !seen {
			values[key] = pyStrip(value)
		}
	}
	port, err := strconv.Atoi(values["API_PORT"])
	if !reConfPort.MatchString(values["API_PORT"]) || err != nil || port < 1 || port > 65535 {
		return 0, kitErr("clusters/"+name+".conf", "invalid API_PORT")
	}
	return port, nil
}

// cleanupDelay bounds the cleanup of the cluster and checks/teardown.sh,
// each, outside the ceiling of the run.
var cleanupDelay = 120 * time.Second

// commonEnv is the environment shared by the checks and the cleanup, besides
// their own SAGNAC_* variables (docs/contrat.md, section 3):
//   - Git reads the repositories of the workspace whatever their owner
//     (safe.directory, the only scope that counts when the global
//     configuration is ignored), with neither core.fsmonitor nor the hooks
//     of the repository;
//   - DOCKER_HOST is kept when it names a local socket: the engine the
//     student chose (rootless Docker, Colima), not always /var/run/docker.sock;
//   - KUBECACHEDIR and DOCKER_CONFIG are kept only under the TMPDIR of the
//     command (set there by the naive student box, whose HOME is masked);
//     elsewhere kubectl and Docker keep those of HOME;
//   - SAGNAC_CLUSTER names the cluster of the lab.
func commonEnv(lab *Lab) []string {
	env := []string{"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*",
		"GIT_CONFIG_KEY_1=core.fsmonitor", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=core.hooksPath", "GIT_CONFIG_VALUE_2=/dev/null"}
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") {
		env = append(env, "DOCKER_HOST="+host)
	}
	tmp := os.Getenv("TMPDIR")
	for _, name := range []string{"KUBECACHEDIR", "DOCKER_CONFIG"} {
		if v := os.Getenv(name); filepath.IsAbs(tmp) && filepath.IsAbs(v) && under(filepath.Clean(v), tmp) {
			env = append(env, name+"="+v)
		}
	}
	if lab.Cluster != "" {
		env = append(env, "SAGNAC_CLUSTER="+lab.Cluster)
	}
	return env
}

// under is pathlib's is_relative_to: p equals base or lies below it,
// component by component.
func under(p, base string) bool {
	parts := func(s string) []string {
		var out []string
		for _, c := range strings.Split(s, "/") {
			if c != "" && c != "." {
				out = append(out, c)
			}
		}
		return out
	}
	pp, bp := parts(p), parts(base)
	return len(pp) >= len(bp) && slices.Equal(pp[:len(bp)], bp)
}

// clusterLock takes check.lock of the cluster without waiting: one check at
// a time per cluster, since all of them work in the same namespace. It
// returns the locked file, or busy when another check holds it; neither
// when the file is missing (cluster never set up: the checks will say
// so). The file is created by make setup and opened read-only (the cache is
// read-only in the naive student box).
func clusterLock(name string) (lock *os.File, busy bool) {
	home, ok := os.LookupEnv("HOME")
	if !ok {
		home = "/nonexistent"
	}
	f, err := os.Open(filepath.Join(home, ".cache", "sagnac", "clusters", name, "check.lock"))
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, true
	}
	return f, false
}

// cleanup is the collection outside processes (docs/contrat.md, section 3):
// lib/cluster-nettoyage.sh of the cadre for a lab with a cluster (before and
// after the run), then checks/teardown.sh of the lab (after only). Same
// reduced environment and network wrapper as the checks, a delay of its own,
// outside the ceiling. It returns the exit codes, -1 for a script that could
// not start or outlived its delay.
func cleanup(lab *Lab, workspace string, seed int64, prefix []string, opening bool) []int {
	var scripts []string
	if lab.Cluster != "" {
		scripts = append(scripts, filepath.Join(lab.cadreDir(), "lib", "cluster-nettoyage.sh"))
	}
	if !opening && lab.Teardown != "" {
		scripts = append(scripts, lab.Teardown)
	}
	var codes []int
	for _, script := range scripts {
		codes = append(codes, runCleanup(lab, script, workspace, seed, prefix))
	}
	return codes
}

func runCleanup(lab *Lab, script, workspace string, seed int64, prefix []string) int {
	tmp, err := os.MkdirTemp("", "sagnac-n-")
	if err != nil {
		return -1
	}
	defer os.RemoveAll(tmp)
	config, err := os.MkdirTemp("", "sagnac-c-")
	if err != nil {
		return -1
	}
	defer os.RemoveAll(config)
	path, found := os.LookupEnv("PATH")
	if !found {
		path = "/usr/bin:/bin"
	}
	home, found := os.LookupEnv("HOME")
	if !found {
		home = tmp
	}
	argv := append(slices.Clone(prefix), script)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workspace
	cmd.Env = append([]string{
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"LC_ALL=C",
		"XDG_CONFIG_HOME=" + config,
		"SAGNAC_LAB=" + lab.ID,
		"SAGNAC_WORKSPACE=" + workspace,
		"SAGNAC_SEED=" + strconv.FormatInt(seed, 10),
		"SAGNAC_LIB=" + lab.libPath,
	}, commonEnv(lab)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() != nil {
		return -1
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	timer := time.NewTimer(cleanupDelay)
	defer timer.Stop()
	rc := -1
	select {
	case <-done:
		rc = cmd.ProcessState.ExitCode()
	case <-timer.C:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
	return rc
}
