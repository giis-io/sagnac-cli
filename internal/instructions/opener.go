package instructions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Replaced by the tests: the system, the search in PATH and the launch.
var (
	goos       = runtime.GOOS
	lookPath   = exec.LookPath
	runCommand = func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...) // no shell: the arguments are not interpreted
		return cmd.Run()
	}
	outputCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
)

const openTimeout = 20 * time.Second

// opener is the program that opens a file or an address: macOS open; wslview
// then explorer.exe under WSL2 (WSL_DISTRO_NAME set); xdg-open elsewhere on
// Linux. "" when the system has none, and the caller shows the path.
func opener() string {
	switch goos {
	case "darwin":
		return found("open")
	case "linux":
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			if p := found("wslview"); p != "" {
				return p
			}
			return found("explorer.exe")
		}
		return found("xdg-open")
	}
	return ""
}

func found(name string) string {
	p, err := lookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// Open opens target (a file of the cache, or a page address the caller has
// checked) with the program of the system. The program is started directly,
// never through a shell, and target is its only argument. It returns false
// when there is nothing to start; an error when the program failed.
func Open(ctx context.Context, target string) (bool, error) {
	if target == "" || strings.HasPrefix(target, "-") {
		return false, ErrForeignPage
	}
	program := opener()
	if program == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	if strings.HasSuffix(program, "explorer.exe") && strings.HasPrefix(target, "/") {
		target = windowsPath(ctx, target)
	}
	err := runCommand(ctx, program, target)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.HasSuffix(program, "explorer.exe") {
		return true, nil // explorer.exe answers 1 when it worked; any other status is a failure
	}
	return err == nil, err
}

// windowsPath gives the Windows form of a Linux path (wslpath -w), or the
// path itself when that cannot be done.
func windowsPath(ctx context.Context, p string) string {
	wslpath := found("wslpath")
	if wslpath == "" {
		return p
	}
	out, err := outputCommand(ctx, wslpath, "-w", p)
	if w := strings.TrimSpace(string(out)); err == nil && w != "" {
		return w
	}
	return p
}
