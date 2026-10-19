package oracle

import (
	"context"
	"os/exec"
	"time"
)

const methodUnshare = "unshare"

// networkWrapper returns the command prefix that runs a check in a new user
// and network namespace (loopback up, no other interface), and the name of
// the method; nil when unshare is missing or not allowed for this user.
// Same wrapper as the reference executor (docs/contrat.md, section 3).
func networkWrapper() ([]string, string) {
	if _, err := exec.LookPath(methodUnshare); err != nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, methodUnshare, "-rn", "true").Run() != nil {
		return nil, ""
	}
	return []string{methodUnshare, "-rn", "sh", "-c", `ip link set lo up 2>/dev/null; exec "$@"`, "sh"}, methodUnshare
}

// NetworkCutAvailable tells whether noNetwork really cuts the network here,
// and with which method.
func NetworkCutAvailable() (string, bool) {
	prefix, method := networkWrapper()
	return method, prefix != nil
}
