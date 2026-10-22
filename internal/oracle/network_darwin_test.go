package oracle

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The profile of the reference executor, character for character: no Unix
// socket for a lab with a cluster, the administrator kubeconfig never
// readable (docs/contrat.md, section 7).
func TestSandboxProfile(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/h\"ome")
	deny := `(deny file-read* file-write* (regex #"^/nonexistent/h\"ome/\.cache/sagnac/clusters/[^/]+/admin(/|$)"))`
	base := `(version 1)(allow default)(deny network-outbound)`
	if got, want := profile(false), base+`(allow network-outbound (remote unix-socket))`+
		`(allow network-outbound (remote ip "localhost:*"))`+deny; got != want {
		t.Errorf("profile(false) =\n%s\nwant\n%s", got, want)
	}
	if got, want := profile(true), base+`(allow network-outbound (remote ip "localhost:*"))`+deny; got != want {
		t.Errorf("profile(true) =\n%s\nwant\n%s", got, want)
	}
}

// Under the cut, a check of a lab with a cluster reaches the API on
// localhost, but neither a Unix socket (the container engine) nor the
// administrator kubeconfig; a lab without a cluster keeps the Unix socket,
// never the administrator kubeconfig.
func TestSandboxCluster(t *testing.T) {
	if _, ok := NetworkCutAvailable(); !ok {
		t.Skip("no sandbox-exec")
	}
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not installed")
	}
	home := clusterHome(t)
	admin := filepath.Join(home, ".cache", "sagnac", "clusters", "sagnac-b1", "admin", "kubeconfig")
	writeFile(t, admin, "apiVersion: v1\n", 0o600)
	api := echoServer(t)
	// Short path: a Unix socket address has 104 bytes at most.
	sockDir, err := exec.Command("mktemp", "-d", "/tmp/skt.XXXXXX").Output()
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(strings.TrimSpace(string(sockDir)), "s")
	t.Cleanup(func() { _ = exec.Command("rm", "-rf", filepath.Dir(sock)).Run() })
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("engine\n"))
			c.Close()
		}
	}()
	probe := `api=no; unix=no; admin=no
printf 'x\n' | nc -w 2 127.0.0.1 ` + strconv.Itoa(api) + ` 2>/dev/null | grep -q echo && api=yes
nc -w 2 -U ` + sock + ` </dev/null 2>/dev/null | grep -q engine && unix=yes
cat "$HOME/.cache/sagnac/clusters/sagnac-b1/admin/kubeconfig" 2>/dev/null | grep -q apiVersion && admin=yes
echo "api=$api unix=$unix admin=$admin" >"$SAGNAC_WORKSPACE/probe"
sagnac_reussite`
	for _, c := range []struct {
		name, meta string
		cut        bool
		want       string
	}{
		{"cluster", clusterMeta, true, "api=yes unix=no admin=no"},
		{"no cluster", "id: x\nversion: 1\nchecks:" + okCheck, true, "api=yes unix=yes admin=no"},
		{"without the cut (the probe sees)", clusterMeta, false, "api=yes unix=yes admin=yes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, ws := clusterFixture(t, c.meta, map[string]string{"one.sh": probe})
			r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, c.cut)
			if err != nil {
				t.Fatal(err)
			}
			if r.ExitCode() != 0 {
				t.Fatalf("checks %v", codes(r))
			}
			if got := strings.TrimSpace(readFile(t, filepath.Join(ws, "probe"))); got != c.want {
				t.Errorf("probe %q, want %q", got, c.want)
			}
		})
	}
	if got := strings.TrimSpace(readFile(t, admin)); got != "apiVersion: v1" {
		t.Errorf("administrator kubeconfig changed: %q", got)
	}
}
