// Package poste reads what "sagnac doctor" checks on the machine: memory,
// free disk space, tools, container engine. It changes nothing on the
// machine, installs nothing and never asks for more rights. A reading that
// cannot be made is reported as such, never as a failure.
package poste

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CommandTimeout bounds each command a reading launches.
const CommandTimeout = 5 * time.Second

// DiskFree returns the bytes a user may still write on the volume of path.
func DiskFree(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Bsize <= 0 {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}

// Tool tells whether the command name can be launched from the PATH.
func Tool(name string) bool {
	path, err := exec.LookPath(name)
	return err == nil && !placeholder(path)
}

// State of the container engine.
const (
	EngineAbsent  = "absent"  // no docker command
	EngineStopped = "stopped" // the command is there, the engine does not answer
	EngineDenied  = "denied"  // the engine refuses this user
	EngineReady   = "ready"
	EngineUnknown = "unknown" // another engine, or an answer that cannot be read
)

// EngineState is what the container engine answers.
type EngineState struct {
	State string
	// Memory is the memory the engine may use, in bytes; MemoryOK tells
	// whether it could be read.
	Memory   uint64
	MemoryOK bool
}

// runner launches a command and returns its outputs; tests replace it.
type runner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

func run(ctx context.Context, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.String(), errOut.String(), err
}

// Engine asks the docker command for the state of the engine. Only
// "docker version" and the memory field of "docker info" are read.
func Engine(ctx context.Context) EngineState {
	return engine(ctx, exec.LookPath, run)
}

func engine(ctx context.Context, look func(string) (string, error), run runner) EngineState {
	if _, err := look("docker"); err != nil {
		if _, err := look("podman"); err == nil {
			return EngineState{State: EngineUnknown}
		}
		return EngineState{State: EngineAbsent}
	}
	out, errOut, err := run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return EngineState{State: engineFailure(errOut, err)}
	}
	if !plainVersion(strings.TrimSpace(out)) {
		return EngineState{State: EngineUnknown}
	}
	st := EngineState{State: EngineReady}
	if out, _, err := run(ctx, "docker", "info", "--format", "{{.MemTotal}}"); err == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64); err == nil && n > 0 {
			st.Memory, st.MemoryOK = n, true
		}
	}
	return st
}

func engineFailure(stderr string, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return EngineStopped
	}
	msg := strings.ToLower(stderr)
	switch {
	case strings.Contains(msg, "permission denied"):
		return EngineDenied
	case strings.Contains(msg, "cannot connect"), strings.Contains(msg, "is the docker daemon running"),
		strings.Contains(msg, "error during connect"), strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such file or directory"):
		return EngineStopped
	}
	return EngineUnknown
}

// plainVersion: a short answer made of the characters of a version number.
func plainVersion(s string) bool {
	if s == "" || len(s) > 64 || s[0] < '0' || s[0] > '9' {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '+' || r == '~') {
			return false
		}
	}
	return true
}
