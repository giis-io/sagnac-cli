package oracle

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
