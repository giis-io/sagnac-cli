package poste

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type answer struct {
	out, errOut string
	err         error
}

func fakeRun(answers map[string]answer) runner {
	return func(_ context.Context, name string, args ...string) (string, string, error) {
		a := answers[args[0]]
		return a.out, a.errOut, a.err
	}
}

func lookOnly(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/usr/local/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
}

func TestEngine(t *testing.T) {
	failed := errors.New("exit status 1")
	cases := []struct {
		name    string
		look    func(string) (string, error)
		answers map[string]answer
		want    EngineState
	}{
		{"no command", lookOnly(), nil, EngineState{State: EngineAbsent}},
		{"podman alone", lookOnly("podman"), nil, EngineState{State: EngineUnknown}},
		{"ready", lookOnly("docker"), map[string]answer{"version": {out: "28.3.2\n"}, "info": {out: "8324055040\n"}},
			EngineState{State: EngineReady, Memory: 8324055040, MemoryOK: true}},
		{"ready, memory unreadable", lookOnly("docker"), map[string]answer{"version": {out: "28.3.2\n"}, "info": {out: "<no value>\n"}},
			EngineState{State: EngineReady}},
		{"ready, info fails", lookOnly("docker"), map[string]answer{"version": {out: "5.4.0-rc1\n"}, "info": {err: failed}},
			EngineState{State: EngineReady}},
		{"ready, zero memory", lookOnly("docker"), map[string]answer{"version": {out: "28.3.2"}, "info": {out: "0"}},
			EngineState{State: EngineReady}},
		{"stopped", lookOnly("docker"), map[string]answer{"version": {errOut: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", err: failed}},
			EngineState{State: EngineStopped}},
		{"socket missing", lookOnly("docker"), map[string]answer{"version": {errOut: "dial unix /run/user/1000/docker.sock: connect: no such file or directory", err: failed}},
			EngineState{State: EngineStopped}},
		{"no answer in time", lookOnly("docker"), map[string]answer{"version": {err: context.DeadlineExceeded}},
			EngineState{State: EngineStopped}},
		{"denied", lookOnly("docker"), map[string]answer{"version": {errOut: "permission denied while trying to connect to the Docker daemon socket", err: failed}},
			EngineState{State: EngineDenied}},
		{"unreadable failure", lookOnly("docker"), map[string]answer{"version": {errOut: "\x1b[31mpanic\x1b[0m", err: failed}},
			EngineState{State: EngineUnknown}},
		{"unreadable answer", lookOnly("docker"), map[string]answer{"version": {out: "<html>proxy</html>"}},
			EngineState{State: EngineUnknown}},
		{"empty answer", lookOnly("docker"), map[string]answer{"version": {out: "\n"}},
			EngineState{State: EngineUnknown}},
	}
	for _, c := range cases {
		if got := engine(context.Background(), c.look, fakeRun(c.answers)); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDiskFree(t *testing.T) {
	if n, ok := DiskFree(t.TempDir()); !ok || n == 0 {
		t.Errorf("DiskFree of a temporary directory = %d, %v", n, ok)
	}
	if _, ok := DiskFree("/nonexistent/sagnac"); ok {
		t.Error("DiskFree of a missing path succeeded")
	}
}

func TestMemoryOfThisMachine(t *testing.T) {
	if n, ok := Memory(); !ok || n < 1<<28 {
		t.Errorf("Memory = %d, %v", n, ok)
	}
}

func TestTool(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if Tool("sagnac-tool") {
		t.Error("a missing tool is reported present")
	}
	if err := os.WriteFile(filepath.Join(dir, "sagnac-tool"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !Tool("sagnac-tool") {
		t.Error("a tool of the PATH is reported missing")
	}
}
