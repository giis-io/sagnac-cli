package oracle

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// sandboxProfile forbids every outgoing connection except to localhost and
// Unix sockets (local model server, Docker). Labs never read the network
// during a check. Same profile as the reference executor (docs/contrat.md,
// section 3).
const sandboxProfile = `(version 1)(allow default)` +
	`(deny network-outbound)` +
	`(allow network-outbound (remote unix-socket))` +
	`(allow network-outbound (remote ip "localhost:*"))`

const methodSandbox = "sandbox-exec"

// needsGateway: the API of a cluster is reachable from the sandbox as it is,
// on localhost.
const needsGateway = false

// adminClustersRegex matches the administrator kubeconfig of the clusters of
// the Sagnac cache (_cadre/bin/sagnac-cluster), never readable by a check or
// by the student's code during check, whatever the lab.
func adminClustersRegex() string {
	home, ok := os.LookupEnv("HOME")
	if !ok {
		home = "/nonexistent"
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	} else if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	return "^" + strings.ReplaceAll(regexp.QuoteMeta(home), `"`, `\"`) + `/\.cache/sagnac/clusters/[^/]+/admin(/|$)`
}

// profile is sandboxProfile for the lab. A lab with a cluster keeps no Unix
// socket (the container engine is not for the checks; the API goes through
// localhost); the others keep the socket of the engine (a check may build an
// image). The administrator kubeconfig is always unreadable.
func profile(cluster bool) string {
	p := sandboxProfile
	if cluster {
		p = strings.Replace(p, `(allow network-outbound (remote unix-socket))`, "", 1)
	}
	return p + `(deny file-read* file-write* (regex #"` + adminClustersRegex() + `"))`
}

// networkWrapper returns the command prefix that cuts the network and the
// name of the method, or nil when sandbox-exec is missing. macOS needs no
// gateway.
func networkWrapper(cluster bool, _ *gateway) ([]string, string) {
	if _, err := exec.LookPath(methodSandbox); err == nil {
		return []string{methodSandbox, "-p", profile(cluster)}, methodSandbox
	}
	return nil, ""
}

// NetworkCutAvailable tells whether noNetwork really cuts the network here,
// and with which method.
func NetworkCutAvailable() (string, bool) {
	prefix, method := networkWrapper(false, nil)
	return method, prefix != nil
}
