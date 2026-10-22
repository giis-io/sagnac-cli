package oracle

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Under the cut, a check of a lab with a cluster reaches the API of the
// cluster through the relay, on the same 127.0.0.1:<port> as on the host,
// and nothing else: neither another port of the host, nor the Internet, nor
// the administrator kubeconfig, nor /run (sockets of the container engine).
// Without a cluster, the administrator kubeconfig stays masked. Run on a
// Linux machine where unshare works for this user (docs/contrat.md, section
// 7): a disposable VM of the factory, never a persistent host.
func TestEnvelopeClusterLinux(t *testing.T) {
	if _, ok := NetworkCutAvailable(); !ok {
		t.Skip("unshare -rnm unshare -Ur not allowed on this machine")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := clusterHome(t)
	writeFile(t, filepath.Join(home, ".cache", "sagnac", "clusters", "sagnac-b1", "admin", "kubeconfig"), "apiVersion: v1\n", 0o600)
	api, other := echoServer(t), echoServer(t)
	body := strconv.Quote(self) + " " + probeCommand + " " + strconv.Itoa(api) + " " + strconv.Itoa(other) +
		` >"$SAGNAC_WORKSPACE/probe" 2>&1` + "\nsagnac_reussite"
	for _, c := range []struct {
		name, meta string
		cut        bool
		want       string
	}{
		{"cluster", clusterMeta, true, "api=yes other=no internet=no admin=no run=no"},
		{"no cluster", "id: x\nversion: 1\nchecks:" + okCheck, true, "api=no other=no internet=no admin=no run=yes"},
		{"without the cut (the probe sees)", clusterMeta, false, "api=yes other=yes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, ws := clusterFixture(t, c.meta, map[string]string{"one.sh": body})
			writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", "sagnac-b1.conf"),
				"API_PORT="+strconv.Itoa(api)+"\n", 0o644)
			r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, c.cut)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(readFile(t, filepath.Join(ws, "probe")))
			if r.ExitCode() != 0 {
				t.Fatalf("checks %v, probe %q", codes(r), got)
			}
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("probe %q, want %q", got, c.want)
			}
		})
	}
}

// The relay goes through the cleanup of the cluster too: before and after
// the run, under the cut, it reaches the API.
func TestEnvelopeClusterCleanupLinux(t *testing.T) {
	if _, ok := NetworkCutAvailable(); !ok {
		t.Skip("unshare -rnm unshare -Ur not allowed on this machine")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clusterHome(t)
	api, other := echoServer(t), echoServer(t)
	dir, ws := clusterFixture(t, clusterMeta, map[string]string{"one.sh": "sagnac_reussite"})
	writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "clusters", "sagnac-b1.conf"), "API_PORT="+strconv.Itoa(api)+"\n", 0o644)
	log := filepath.Join(t.TempDir(), "cleanup")
	writeFile(t, filepath.Join(filepath.Dir(dir), "_cadre", "lib", "cluster-nettoyage.sh"),
		"#!/bin/sh\n"+strconv.Quote(self)+" "+probeCommand+" "+strconv.Itoa(api)+" "+strconv.Itoa(other)+" >>"+log+" 2>&1\n", 0o755)
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, true)
	if err != nil || r.ExitCode() != 0 {
		t.Fatalf("Verify: %v %v", err, codes(r))
	}
	lines := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	if len(lines) != 2 {
		t.Fatalf("cleanup ran %d times: %q", len(lines), lines)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "api=yes other=no internet=no admin=no run=no") {
			t.Errorf("cleanup probe %q", l)
		}
	}
}
