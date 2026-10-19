package oracle

import "os/exec"

// sandboxProfile forbids every outgoing connection except to localhost and
// Unix sockets (local model server, Docker). Labs never read the network
// during a check. Same profile as the reference executor (docs/contrat.md,
// section 3).
const sandboxProfile = `(version 1)(allow default)` +
	`(deny network-outbound)` +
	`(allow network-outbound (remote unix-socket))` +
	`(allow network-outbound (remote ip "localhost:*"))`

const methodSandbox = "sandbox-exec"

// networkWrapper returns the command prefix that cuts the network and the
// name of the method, or nil when sandbox-exec is missing.
func networkWrapper() ([]string, string) {
	if _, err := exec.LookPath(methodSandbox); err == nil {
		return []string{methodSandbox, "-p", sandboxProfile}, methodSandbox
	}
	return nil, ""
}

// NetworkCutAvailable tells whether noNetwork really cuts the network here,
// and with which method.
func NetworkCutAvailable() (string, bool) {
	prefix, method := networkWrapper()
	return method, prefix != nil
}
