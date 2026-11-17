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

// ldconfigCache is what "ldconfig -p" prints on Ubuntu 24.04, shortened.
const ldconfigCache = "438 libs found in cache `/etc/ld.so.cache'\n" +
	"\tlibz.so.1 (libc6,x86-64) => /lib/x86_64-linux-gnu/libz.so.1\n" +
	"\tlibc.so.6 (libc6,x86-64) => /lib/x86_64-linux-gnu/libc.so.6\n"

const ldconfigGomp = "\tlibgomp.so.1 (libc6,x86-64) => /lib/x86_64-linux-gnu/libgomp.so.1\n"

func TestLibrary(t *testing.T) {
	notFound := errors.New(`exec: "ldconfig": executable file not found in $PATH`)
	empty := t.TempDir()
	plain := t.TempDir()
	multiarch := t.TempDir()
	other := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(plain, "libgomp.so.1"):                         "",
		filepath.Join(multiarch, "x86_64-linux-gnu", "libgomp.so.1"): "",
		filepath.Join(other, "x86_64-linux-gnu", "libgomp.so.1.0.0"): "",
		filepath.Join(other, "gcc", "libgomp.so.1"):                  "",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with the name of the library is not the library.
	if err := os.MkdirAll(filepath.Join(other, "libgomp.so.1"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		ldconfig  map[string]answer // by command
		roots     []string
		found, ok bool
	}{
		{"listed by ldconfig", map[string]answer{"ldconfig": {out: ldconfigCache + ldconfigGomp}}, []string{empty}, true, true},
		{"absent", map[string]answer{"ldconfig": {out: ldconfigCache}}, []string{empty, other}, false, true},
		{"another name that starts the same", map[string]answer{"ldconfig": {out: ldconfigCache + "\tlibgomp.so.10 (libc6,x86-64) => /lib/libgomp.so.10\n"}},
			[]string{empty}, false, true},
		{"not in the cache, in a usual directory", map[string]answer{"ldconfig": {out: ldconfigCache}}, []string{empty, plain}, true, true},
		{"ldconfig outside the PATH", map[string]answer{"ldconfig": {err: notFound}, "/usr/sbin/ldconfig": {out: ldconfigCache + ldconfigGomp}},
			[]string{empty}, true, true},
		{"ldconfig outside the PATH, absent", map[string]answer{"ldconfig": {err: notFound}, "/sbin/ldconfig": {out: ldconfigCache}},
			[]string{empty}, false, true},
		{"no ldconfig, in a usual directory", nil, []string{empty, plain}, true, true},
		{"no ldconfig, in a multiarch directory", nil, []string{multiarch}, true, true},
		{"no ldconfig, nowhere", nil, []string{empty, other, "/nonexistent/sagnac"}, false, false},
		{"ldconfig fails", map[string]answer{"ldconfig": {out: ldconfigCache + ldconfigGomp, err: errors.New("exit status 1")}}, []string{empty}, false, false},
		{"ldconfig prints no cache", map[string]answer{"ldconfig": {out: "ldconfig: unrecognized option: p\n"}}, []string{empty}, false, false},
		{"no answer in time", map[string]answer{"ldconfig": {err: context.DeadlineExceeded}, "/sbin/ldconfig": {out: ldconfigCache}}, []string{empty}, false, false},
	}
	for _, c := range cases {
		var launched []string
		run := func(_ context.Context, name string, args ...string) (string, string, error) {
			launched = append(launched, name)
			if len(args) != 1 || args[0] != "-p" {
				t.Errorf("%s: %s launched with %v", c.name, name, args)
			}
			a, known := c.ldconfig[name]
			if !known {
				return "", "", errors.New("not found")
			}
			return a.out, a.errOut, a.err
		}
		if found, ok := library(context.Background(), "libgomp.so.1", run, c.roots); found != c.found || ok != c.ok {
			t.Errorf("%s: %v, %v; want %v, %v (launched %v)", c.name, found, ok, c.found, c.ok, launched)
		}
		if c.name == "no answer in time" && len(launched) != 1 {
			t.Errorf("%s: launched %v", c.name, launched)
		}
	}
}
