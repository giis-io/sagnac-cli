package oracle

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Relay of the API of the cluster through the network cut of check under
// Linux (docs/contrat.md, section 7). unshare -rn gives the check an empty
// network namespace: the 127.0.0.1 of the host, where k3d publishes the
// Kubernetes API, is not there. Outside that namespace, the command opens a
// Unix socket in a private directory, tied to the port of the API only
// (gateway). Inside it, the same binary started as RelayCommand brings lo up,
// listens on 127.0.0.1:<port>, sends every connection to that socket, runs
// the check and returns its code. The kubeconfig of the cluster stays valid
// as it is. Nothing else goes through: neither the Internet, nor the socket
// of the container engine, nor another port of the host.

// RelayCommand is the hidden first argument that makes the sagnac binary run
// as the relay inside the network namespace of a check. The command line
// dispatches it before anything else.
const RelayCommand = "__cluster-relay"

const (
	relayBuffer      = 65536
	relayMaxConns    = 32 // simultaneous connections per relay; beyond, refused
	relayDialTimeout = 10 * time.Second
)

// relayExecutable is the binary started as the relay: this one.
var relayExecutable = os.Executable

// pump copies a to b, then closes the write side of b and the read side of a.
func pump(a, b net.Conn) {
	buf := make([]byte, relayBuffer)
	_, _ = io.CopyBuffer(b, a, buf)
	if w, ok := b.(interface{ CloseWrite() error }); ok {
		_ = w.CloseWrite()
	}
	if r, ok := a.(interface{ CloseRead() error }); ok {
		_ = r.CloseRead()
	}
}

// link copies both ways until both sides are closed, then closes them.
func link(a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		pump(b, a)
		close(done)
	}()
	pump(a, b)
	<-done
	a.Close()
	b.Close()
}

// serve accepts connections on l and links each one to a new connection
// made by dial, at most relayMaxConns at a time; it returns when l is closed.
func serve(l net.Listener, dial func() (net.Conn, error)) {
	slots := make(chan struct{}, relayMaxConns)
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			upstream, err := dial()
			if err != nil {
				conn.Close()
				return
			}
			link(conn, upstream)
		}()
	}
}

// dialAPI joins the API, the connection only bounded by relayDialTimeout:
// a slow stream (kubectl exec -i pushing a file to a slow pod) is never cut.
func dialAPI(port int) (net.Conn, error) {
	return net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), relayDialTimeout)
}

// gateway is the host side: a Unix socket in a private directory, linked to
// 127.0.0.1:<port> of the host (the API of the cluster, nothing else).
type gateway struct {
	port     int
	dir      string
	path     string
	listener net.Listener
	once     sync.Once
}

// openGateway opens the socket in a new 0700 directory under TMPDIR, or
// under /tmp when that path would exceed the size of a Unix socket address.
func openGateway(port int) (*gateway, error) {
	if port < 1 || port > 65535 {
		return nil, syscall.EINVAL
	}
	base := os.TempDir()
	if len(filepath.Join(base, "skXXXXXXXX", "api")) > 100 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "sk")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	g := &gateway{port: port, dir: dir, path: filepath.Join(dir, "api")}
	if g.listener, err = net.Listen("unix", g.path); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := os.Chmod(g.path, 0o600); err != nil {
		g.close()
		return nil, err
	}
	go serve(g.listener, func() (net.Conn, error) { return dialAPI(g.port) })
	return g, nil
}

// close stops accepting and removes the private directory.
func (g *gateway) close() {
	g.once.Do(func() {
		g.listener.Close()
		os.RemoveAll(g.dir)
	})
}

// RunRelay is the relay inside the network namespace of a check:
//
//	__cluster-relay --lo-up
//	__cluster-relay --port P --socket S -- command [args...]
//
// The first form brings lo up and exits (wrapper without ip). The second
// brings lo up if needed, listens on 127.0.0.1:P, sends each connection to
// the Unix socket S, runs the command with an empty input and returns its
// exit code (128 + signal for a killed command, 126 when it cannot start).
func RunRelay(args []string) int {
	var port int
	var socket string
options:
	for len(args) > 0 {
		switch {
		case args[0] == "--lo-up":
			if liftLoopback() != nil {
				return 1
			}
			return 0
		case args[0] == "--port" && len(args) > 1:
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 || n > 65535 {
				return 64
			}
			port, args = n, args[2:]
		case args[0] == "--socket" && len(args) > 1:
			socket, args = args[1], args[2:]
		default:
			break options
		}
	}
	for len(args) > 0 && args[0] == "--" { // separators of the wrapper and of the relay
		args = args[1:]
	}
	if port == 0 || socket == "" || len(args) == 0 {
		return 64
	}
	_ = liftLoopback() // already up when the wrapper had ip
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return 125
	}
	defer l.Close()
	go serve(l, func() (net.Conn, error) { return net.Dial("unix", socket) })
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return 126
	}
	_ = cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}
