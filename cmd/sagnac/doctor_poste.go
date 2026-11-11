package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/oracle"
	"github.com/giis-io/sagnac-cli/internal/poste"
)

// What the path needs from the machine. The figures are the ones of the
// offer: 8 GB of memory at least and 16 advised, 30 GB of free disk and 40
// advised, 4 GB for the container engine. A machine sold with 8 GB shows a
// little less to the system, so does an engine given 4 GB: the thresholds
// sit just below the figure.
const (
	gib = 1 << 30
	gb  = 1000 * 1000 * 1000

	memoryMinimum     = 7 * gib
	memoryRecommended = 15 * gib
	diskMinimum       = 30 * gb
	diskRecommended   = 40 * gb
	engineMemoryMin   = 7 * gib / 2

	// machineChecksTimeout bounds the checks of the machine as a whole.
	machineChecksTimeout = 20 * time.Second
)

// requiredTools are looked up in the PATH, in this order.
var requiredTools = []string{"git", "curl", "jq", "make", "python3"}

// hostProbe reads the machine; tests replace it.
type hostProbe struct {
	goos, goarch string
	wsl          func() bool
	memory       func() (uint64, bool)
	diskFree     func() (uint64, bool)
	tool         func(name string) bool
	engine       func(ctx context.Context) poste.EngineState
	networkCut   func() bool
}

var host = hostProbe{
	goos:   runtime.GOOS,
	goarch: runtime.GOARCH,
	wsl:    poste.WSL,
	memory: poste.Memory,
	diskFree: func() (uint64, bool) {
		home, err := os.UserHomeDir()
		if err != nil {
			return 0, false
		}
		return poste.DiskFree(home)
	},
	tool:   poste.Tool,
	engine: poste.Engine,
	networkCut: func() bool {
		_, ok := oracle.NetworkCutAvailable()
		return ok
	},
}

// oneDecimal writes bytes in units of unit with one decimal, French comma,
// rounded down: a machine just under a threshold is never shown at it.
func oneDecimal(bytes, unit uint64) string {
	tenths := bytes/unit*10 + bytes%unit*10/unit
	if tenths%10 == 0 {
		return strconv.FormatUint(tenths/10, 10)
	}
	return fmt.Sprintf("%d,%d", tenths/10, tenths%10)
}

func systemName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	}
	return goos
}

// machineChecks reports the state of the machine, one line per point on
// stdout, and returns the checks of the report. Measured values are shown,
// never put in a check. A reading that fails gives a check "skip".
func machineChecks(ctx context.Context, stdout io.Writer) []doctorCheck {
	ctx, cancel := context.WithTimeout(ctx, machineChecksTimeout)
	defer cancel()
	var checks []doctorCheck
	add := func(id, status, code, line string) {
		checks = append(checks, doctorCheck{ID: id, Status: status, Code: code})
		fmt.Fprintln(stdout, line)
	}

	if host.goos == "linux" && host.wsl() {
		add("os", checkWarn, "WSL_NOT_SUPPORTED", i18n.T("doctor.os_wsl"))
	} else {
		add("os", checkOK, "", i18n.T("doctor.os_ok", systemName(host.goos), host.goarch))
	}

	switch mem, ok := host.memory(); {
	case !ok:
		add("memory", checkSkip, "", i18n.T("doctor.memory_skip"))
	case mem < memoryMinimum:
		add("memory", checkFail, "MEMORY_BELOW_MINIMUM", i18n.T("doctor.memory_fail", oneDecimal(mem, gib)))
	case mem < memoryRecommended:
		add("memory", checkWarn, "MEMORY_BELOW_RECOMMENDED", i18n.T("doctor.memory_low", oneDecimal(mem, gib)))
	default:
		add("memory", checkOK, "", i18n.T("doctor.memory_ok", oneDecimal(mem, gib)))
	}

	switch free, ok := host.diskFree(); {
	case !ok:
		add("disk", checkSkip, "", i18n.T("doctor.disk_skip"))
	case free < diskMinimum:
		add("disk", checkFail, "DISK_BELOW_MINIMUM", i18n.T("doctor.disk_fail", free/gb))
	case free < diskRecommended:
		add("disk", checkWarn, "DISK_BELOW_RECOMMENDED", i18n.T("doctor.disk_low", free/gb))
	default:
		add("disk", checkOK, "", i18n.T("doctor.disk_ok", free/gb))
	}

	var found []string
	for _, name := range requiredTools {
		id := "tool_" + name
		if host.tool(name) {
			checks = append(checks, doctorCheck{ID: id, Status: checkOK})
			found = append(found, name)
		} else {
			add(id, checkFail, "TOOL_MISSING", i18n.T("doctor.tool_missing", name))
		}
	}
	if len(found) > 0 {
		fmt.Fprintln(stdout, i18n.T("doctor.tools_ok", strings.Join(found, ", ")))
	}

	engine := host.engine(ctx)
	switch engine.State {
	case poste.EngineReady:
		add("container_engine", checkOK, "", i18n.T("doctor.engine_ok"))
	case poste.EngineAbsent:
		add("container_engine", checkWarn, "ENGINE_NOT_INSTALLED", i18n.T("doctor.engine_absent"))
	case poste.EngineStopped:
		add("container_engine", checkFail, "ENGINE_NOT_RUNNING", i18n.T("doctor.engine_stopped"))
	case poste.EngineDenied:
		add("container_engine", checkFail, "ENGINE_PERMISSION_DENIED", i18n.T("doctor.engine_denied"))
	default:
		add("container_engine", checkSkip, "", i18n.T("doctor.engine_skip"))
	}
	switch {
	case engine.State != poste.EngineReady:
		// Said by the line of the engine: nothing more to show.
		checks = append(checks, doctorCheck{ID: "container_memory", Status: checkSkip})
	case !engine.MemoryOK:
		add("container_memory", checkSkip, "", i18n.T("doctor.engine_memory_skip"))
	case engine.Memory < engineMemoryMin:
		add("container_memory", checkWarn, "ENGINE_MEMORY_LOW", i18n.T("doctor.engine_memory_low", oneDecimal(engine.Memory, gib)))
	default:
		add("container_memory", checkOK, "", i18n.T("doctor.engine_memory_ok", oneDecimal(engine.Memory, gib)))
	}

	if host.networkCut() {
		add("network_cut", checkOK, "", i18n.T("doctor.network_cut_ok"))
	} else {
		add("network_cut", checkWarn, "NETWORK_CUT_UNAVAILABLE", i18n.T("doctor.network_cut_unavailable"))
	}
	return checks
}
