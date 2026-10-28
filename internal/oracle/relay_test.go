package oracle

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary is the relay of a check under Linux (RunRelay), like the
// sagnac binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == RelayCommand {
		os.Exit(RunRelay(os.Args[2:]))
	}
	if len(os.Args) > 2 && os.Args[1] == probeCommand {
		os.Exit(probe(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// probeCommand makes the test binary a probe run by a check under the
// network cut: probe API_PORT OTHER_PORT prints what it reaches, as
// key=yes|no pairs.
const probeCommand = "__oracle-probe"

func probe(args []string) int {
	reach := func(addr string) string {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return "no"
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.WriteString(c, "x\n"); err != nil {
			return "no"
		}
		if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.HasPrefix(line, "echo") {
			return "no"
		}
		return "yes"
	}
	yes := func(ok bool) string {
		if ok {
			return "yes"
		}
		return "no"
	}
	_, adminErr := os.ReadFile(os.Getenv("HOME") + "/.cache/sagnac/clusters/sagnac-b1/admin/kubeconfig")
	entries, _ := os.ReadDir("/run")
	internet, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
	if err == nil {
		internet.Close()
	}
	fmt.Printf("api=%s other=%s internet=%s admin=%s run=%s\n", reach("127.0.0.1:"+args[0]), reach("127.0.0.1:"+args[1]),
		yes(err == nil), yes(adminErr == nil), yes(len(entries) > 0))
	return 0
}

// echoServer answers every line with "echo <line>" on 127.0.0.1, standing for
// the API of the cluster.
func echoServer(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := io.WriteString(c, "echo "+line); err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func roundTrip(t *testing.T, network, addr, line string) string {
	t.Helper()
	c, err := net.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read through %s: %v", addr, err)
	}
	return strings.TrimSpace(got)
}

// The gateway links its private Unix socket to the port of the API, and to
// nothing else; its directory is private and removed when it closes.
func TestGateway(t *testing.T) {
	api := echoServer(t)
	g, err := openGateway(api)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(g.dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("directory of the gateway: %v %v, want 0700", info.Mode(), err)
	}
	if info, err := os.Stat(g.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("socket of the gateway: %v, want 0600", err)
	}
	if len(g.path) > 104 {
		t.Errorf("socket path of %d bytes", len(g.path))
	}
	if got := roundTrip(t, "unix", g.path, "ping"); got != "echo ping" {
		t.Errorf("through the gateway: %q", got)
	}
	g.close()
	if _, err := os.Stat(g.dir); !os.IsNotExist(err) {
		t.Errorf("directory of the gateway left: %v", err)
	}
	if _, err := openGateway(0); err == nil {
		t.Error("gateway on port 0")
	}
}

// relayCmd runs RunRelay in this process, as the namespace would, and gives
// its code.
func relayCmd(args ...string) chan int {
	done := make(chan int, 1)
	go func() { done <- RunRelay(args) }()
	return done
}

// The relay listens on 127.0.0.1:<port>, sends each connection to the
// socket of the gateway, runs the check and returns its code.
func TestRelay(t *testing.T) {
	api := echoServer(t)
	g, err := openGateway(api)
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	port := freePort(t)
	done := relayCmd("--port", strconv.Itoa(port), "--socket", g.path, "--", "sh", "-c", "sleep 2; exit 3")
	var got string
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			c.Close()
			got = roundTrip(t, "tcp", "127.0.0.1:"+strconv.Itoa(port), "get pods")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != "echo get pods" {
		t.Errorf("through the relay: %q", got)
	}
	if rc := <-done; rc != 3 {
		t.Errorf("relay code %d, want the code of the check (3)", rc)
	}

	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"killed check", []string{"--port", strconv.Itoa(freePort(t)), "--socket", g.path, "--", "sh", "-c", "kill -9 $$"}, 137},
		{"missing command", []string{"--port", strconv.Itoa(freePort(t)), "--socket", g.path, "--", "/nonexistent/check"}, 126},
		{"no command", []string{"--port", "1", "--socket", g.path, "--"}, 64},
		{"no socket", []string{"--port", "1", "--", "true"}, 64},
		{"bad port", []string{"--port", "70000", "--socket", g.path, "--", "true"}, 64},
		{"separators", []string{"--port", strconv.Itoa(freePort(t)), "--socket", g.path, "--", "--", "true"}, 0},
	} {
		if rc := <-relayCmd(c.args...); rc != c.want {
			t.Errorf("%s: code %d, want %d", c.name, rc, c.want)
		}
	}
}

// At most relayMaxConns connections at a time: beyond, closed at once.
func TestRelayMaxConnections(t *testing.T) {
	api := echoServer(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serve(l, func() (net.Conn, error) { return dialAPI(api) })
	addr := l.Addr().String()
	var open []net.Conn
	defer func() {
		for _, c := range open {
			c.Close()
		}
	}()
	for i := 0; i < relayMaxConns; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(c, "x\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
			t.Fatalf("connection %d: %v", i+1, err)
		}
	}
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	_ = extra.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(extra, "x\n")
	if _, err := bufio.NewReader(extra).ReadString('\n'); err == nil {
		t.Errorf("connection %d went through", relayMaxConns+1)
	}
}
