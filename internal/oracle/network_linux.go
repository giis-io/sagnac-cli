package oracle

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

const methodUnshare = "unshare"

// needsGateway: the network namespace of a check has no 127.0.0.1 of the
// host; the API of a cluster goes through the relay.
const needsGateway = true

// Masks set in the mount namespace of the check (unshare -m), before the
// check; a failed mount stops everything (code 125, ORACLE_CHECK_INTERRUPTED),
// never a check without its mask. Then the check enters a CHILD user
// namespace (unshare -Ur): root there, but the mount namespace belongs to
// the parent, so neither umount nor mount; and in a mount namespace of its
// own the masks are locked. /proc/<pid>/root of a host process stays
// refused (another user namespace). Same wrapper as the reference executor
// (docs/contrat.md, sections 3 and 7).
const (
	// The administrator kubeconfig of every cluster of the Sagnac cache.
	masksLinux = `for d in "$HOME"/.cache/sagnac/clusters/*/admin; do ` +
		`[ -d "$d" ] || continue; mount -t tmpfs -o size=4k,mode=000 sagnac "$d" || exit 125; done; `
	// Lab with a cluster: the Unix sockets of /run (container engine,
	// rootless included) are masked.
	masksRunLinux = `mount -t tmpfs -o size=64k sagnac /run || exit 125; ` +
		`[ -L /var/run ] || mount -t tmpfs -o size=64k sagnac /var/run || exit 125; `
)

// networkWrapper returns the command prefix that runs a check in new user,
// network and mount namespaces (loopback up, no other interface, masks set),
// then in a child user namespace, and the name of the method; nil when
// unshare is missing or not allowed for this user. With a gateway, the relay
// of the API of the cluster runs the check inside the namespace. lo is
// brought up by ip, or by this binary when ip is missing ($0 of the script).
func networkWrapper(cluster bool, gw *gateway) ([]string, string) {
	if _, err := exec.LookPath(methodUnshare); err != nil {
		return nil, ""
	}
	self, err := relayExecutable()
	if err != nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, methodUnshare, "-rnm", methodUnshare, "-Ur", "true").Run() != nil {
		return nil, ""
	}
	script := masksLinux
	if cluster {
		script += masksRunLinux
	}
	script += `ip link set lo up 2>/dev/null || "$0" ` + RelayCommand + ` --lo-up || exit 125; exec unshare -Ur "$@"`
	prefix := []string{methodUnshare, "-rnm", "sh", "-c", script, self}
	if gw != nil {
		prefix = append(prefix, self, RelayCommand, "--port", strconv.Itoa(gw.port), "--socket", gw.path, "--")
	}
	return prefix, methodUnshare
}

// NetworkCutAvailable tells whether noNetwork really cuts the network here,
// and with which method.
func NetworkCutAvailable() (string, bool) {
	prefix, method := networkWrapper(false, nil)
	return method, prefix != nil
}
