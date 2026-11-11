package main

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/poste"
)

// healthyHost is a machine with nothing to report.
func healthyHost() hostProbe {
	return hostProbe{
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		wsl:      func() bool { return false },
		memory:   func() (uint64, bool) { return 16 * gib, true },
		diskFree: func() (uint64, bool) { return 100 * gb, true },
		tool:     func(string) bool { return true },
		engine: func(context.Context) poste.EngineState {
			return poste.EngineState{State: poste.EngineReady, Memory: 8 * gib, MemoryOK: true}
		},
		networkCut: func() bool { return true },
	}
}

// withHost runs doctor on a machine changed by change.
func withHost(t *testing.T, change func(h *hostProbe), args ...string) (int, string, string) {
	t.Helper()
	old := host
	h := healthyHost()
	change(&h)
	host = h
	t.Cleanup(func() { host = old })
	isolate(t, newVersionServer(t, fileOf("1.2.3")), day)
	return execute(t, append([]string{"doctor"}, args...)...)
}

func checkOf(checks []doctorCheck, id string) doctorCheck {
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	return doctorCheck{}
}

func TestMachineChecks(t *testing.T) {
	engine := func(state string, memory uint64, ok bool) func(*hostProbe) {
		return func(h *hostProbe) {
			h.engine = func(context.Context) poste.EngineState {
				return poste.EngineState{State: state, Memory: memory, MemoryOK: ok}
			}
		}
	}
	cases := []struct {
		name   string
		change func(h *hostProbe)
		want   []doctorCheck // the checks that differ from a healthy machine
		toFix  int
		shown  string
	}{
		{"healthy", func(*hostProbe) {}, nil, 0, "Rien à corriger."},
		{"wsl", func(h *hostProbe) { h.goos = "linux"; h.wsl = func() bool { return true } },
			[]doctorCheck{{"os", checkWarn, "WSL_NOT_SUPPORTED"}}, 0, "WSL"},
		{"wsl is never read on macOS", func(h *hostProbe) { h.goos = "darwin"; h.wsl = func() bool { return true } }, nil, 0, "macOS"},
		{"memory unreadable", func(h *hostProbe) { h.memory = func() (uint64, bool) { return 0, false } },
			[]doctorCheck{{"memory", checkSkip, ""}}, 0, "Non vérifié"},
		{"memory under 7 GiB", func(h *hostProbe) { h.memory = func() (uint64, bool) { return 7*gib - 1, true } },
			[]doctorCheck{{"memory", checkFail, "MEMORY_BELOW_MINIMUM"}}, 1, "6,9 Go"},
		{"memory at 7 GiB", func(h *hostProbe) { h.memory = func() (uint64, bool) { return 7 * gib, true } },
			[]doctorCheck{{"memory", checkWarn, "MEMORY_BELOW_RECOMMENDED"}}, 0, "7 Go"},
		{"memory of a machine sold with 8 GB", func(h *hostProbe) { h.memory = func() (uint64, bool) { return 8162 << 20, true } },
			[]doctorCheck{{"memory", checkWarn, "MEMORY_BELOW_RECOMMENDED"}}, 0, "7,9 Go"},
		{"memory under 15 GiB", func(h *hostProbe) { h.memory = func() (uint64, bool) { return 15*gib - 1, true } },
			[]doctorCheck{{"memory", checkWarn, "MEMORY_BELOW_RECOMMENDED"}}, 0, "14,9 Go"},
		{"memory at 15 GiB", func(h *hostProbe) { h.memory = func() (uint64, bool) { return 15 * gib, true } }, nil, 0, "Mémoire : 15 Go."},
		{"disk unreadable", func(h *hostProbe) { h.diskFree = func() (uint64, bool) { return 0, false } },
			[]doctorCheck{{"disk", checkSkip, ""}}, 0, "Non vérifié"},
		{"disk under 30 GB", func(h *hostProbe) { h.diskFree = func() (uint64, bool) { return 30*gb - 1, true } },
			[]doctorCheck{{"disk", checkFail, "DISK_BELOW_MINIMUM"}}, 1, "29 Go libres"},
		{"disk at 30 GB", func(h *hostProbe) { h.diskFree = func() (uint64, bool) { return 30 * gb, true } },
			[]doctorCheck{{"disk", checkWarn, "DISK_BELOW_RECOMMENDED"}}, 0, "30 Go libres"},
		{"disk under 40 GB", func(h *hostProbe) { h.diskFree = func() (uint64, bool) { return 40*gb - 1, true } },
			[]doctorCheck{{"disk", checkWarn, "DISK_BELOW_RECOMMENDED"}}, 0, "39 Go libres"},
		{"disk at 40 GB", func(h *hostProbe) { h.diskFree = func() (uint64, bool) { return 40 * gb, true } }, nil, 0, "Disque : 40 Go libres."},
		{"jq and make missing", func(h *hostProbe) { h.tool = func(n string) bool { return n != "jq" && n != "make" } },
			[]doctorCheck{{"tool_jq", checkFail, "TOOL_MISSING"}, {"tool_make", checkFail, "TOOL_MISSING"}}, 2, "Outils : git, curl, python3."},
		{"engine absent", engine(poste.EngineAbsent, 0, false),
			[]doctorCheck{{"container_engine", checkWarn, "ENGINE_NOT_INSTALLED"}, {"container_memory", checkSkip, ""}}, 0, "Le premier lab s'en passe"},
		{"engine stopped", engine(poste.EngineStopped, 0, false),
			[]doctorCheck{{"container_engine", checkFail, "ENGINE_NOT_RUNNING"}, {"container_memory", checkSkip, ""}}, 1, "ne répond pas"},
		{"engine denied", engine(poste.EngineDenied, 0, false),
			[]doctorCheck{{"container_engine", checkFail, "ENGINE_PERMISSION_DENIED"}, {"container_memory", checkSkip, ""}}, 1, "pas le droit"},
		{"engine unreadable", engine(poste.EngineUnknown, 0, false),
			[]doctorCheck{{"container_engine", checkSkip, ""}, {"container_memory", checkSkip, ""}}, 0, "Non vérifié"},
		{"engine with an unknown state", engine("", 0, false),
			[]doctorCheck{{"container_engine", checkSkip, ""}, {"container_memory", checkSkip, ""}}, 0, "Non vérifié"},
		{"engine memory unreadable", engine(poste.EngineReady, 0, false),
			[]doctorCheck{{"container_memory", checkSkip, ""}}, 0, "Non vérifié"},
		{"engine memory low", engine(poste.EngineReady, 2*gib, true),
			[]doctorCheck{{"container_memory", checkWarn, "ENGINE_MEMORY_LOW"}}, 0, "2 Go de mémoire pour le moteur"},
		{"engine given 4 GB", engine(poste.EngineReady, 4101881856, true), nil, 0, "Mémoire du moteur de conteneurs : 3,8 Go."},
		{"no network cut", func(h *hostProbe) { h.networkCut = func() bool { return false } },
			[]doctorCheck{{"network_cut", checkWarn, "NETWORK_CUT_UNAVAILABLE"}}, 0, "Ne désactivez aucune protection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			checks := func() []doctorCheck {
				old := host
				defer func() { host = old }()
				h := healthyHost()
				c.change(&h)
				host = h
				return machineChecks(context.Background(), &out)
			}()
			ids := []string{"os", "memory", "disk", "tool_git", "tool_curl", "tool_jq", "tool_make", "tool_python3",
				"container_engine", "container_memory", "network_cut"}
			if len(checks) != len(ids) {
				t.Fatalf("%d checks, want %d", len(checks), len(ids))
			}
			toFix := 0
			for i, id := range ids {
				want := doctorCheck{ID: id, Status: checkOK}
				if w := checkOf(c.want, id); w.ID != "" {
					want = w
				}
				if checks[i] != want {
					t.Errorf("check %d = %+v, want %+v", i, checks[i], want)
				}
				if checks[i].Status == checkFail {
					toFix++
				}
			}
			code, stdout, stderr := withHost(t, c.change)
			wantCode := exitOK
			if c.toFix > 0 {
				wantCode = exitFailure
			}
			if toFix != c.toFix || code != wantCode {
				t.Errorf("%d points to fix, code %d; want %d, code %d (stderr %q)", toFix, code, c.toFix, wantCode, stderr)
			}
			if !strings.Contains(stdout, c.shown) || !strings.Contains(stdout, out.String()) {
				t.Errorf("stdout lacks %q:\n%s", c.shown, stdout)
			}
			if c.toFix > 0 && !strings.Contains(stdout, i18n.T("doctor.to_fix", c.toFix)) {
				t.Errorf("stdout lacks the count of points to fix:\n%s", stdout)
			}
		})
	}
}

// A message that asks for something says where to read how.
func TestMachineMessagesPointToTheGuide(t *testing.T) {
	for _, k := range i18n.Keys() {
		msg := i18n.T(k)
		machine := strings.HasPrefix(k, "doctor.os_") || strings.HasPrefix(k, "doctor.memory_") || strings.HasPrefix(k, "doctor.disk_") ||
			strings.HasPrefix(k, "doctor.tool") || strings.HasPrefix(k, "doctor.engine_") || strings.HasPrefix(k, "doctor.network_cut_")
		if !machine || !(strings.HasPrefix(msg, "Attention") || strings.HasPrefix(msg, "À corriger")) {
			continue
		}
		if !strings.Contains(msg, "« Préparer votre poste », section ") {
			t.Errorf("%s does not name a section of the guide: %q", k, msg)
		}
		if strings.Contains(msg, "sudo") {
			t.Errorf("%s mentions sudo", k)
		}
	}
}

// The report carries an id, a status and a code per check: no measured
// value, no path, no name of the machine.
func TestMachineReportHoldsNoMeasure(t *testing.T) {
	rsHost := func(h *hostProbe) {
		h.memory = func() (uint64, bool) { return 7654321 << 10, true }
		h.diskFree = func() (uint64, bool) { return 31234567890, true }
		h.engine = func(context.Context) poste.EngineState {
			return poste.EngineState{State: poste.EngineReady, Memory: 2087654321, MemoryOK: true}
		}
	}
	old := host
	h := healthyHost()
	rsHost(&h)
	host = h
	t.Cleanup(func() { host = old })
	isolate(t, newVersionServer(t, fileOf("1.2.3")), day)
	rs := newReportServer(t)
	if code, stdout, stderr := execute(t, "doctor", "--share"); code != exitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Outside the fixed names, the report holds no digit at all.
	raw := strings.NewReplacer("sagnac-doctor/1", "", "sagnac/1.2.3", "", "tool_python3", "", "arm64", "", "amd64", "").Replace(string(rs.bodies[0]))
	if strings.ContainsAny(raw, "0123456789/") || strings.Contains(raw, "Go") {
		t.Errorf("the report holds a measure or a path: %s", raw)
	}
	var report struct {
		Checks []map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rs.bodies[0], &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 13 || len(report.Checks) > maxDoctorChecks {
		t.Errorf("%d checks", len(report.Checks))
	}
	got := map[string]string{}
	for _, c := range report.Checks {
		for field := range c {
			if field != "id" && field != "status" && field != "code" {
				t.Errorf("field %q in a check", field)
			}
		}
		if !reCheckID.MatchString(c["id"]) || (c["code"] != "" && !reCheckCode.MatchString(c["code"])) {
			t.Errorf("check outside the schema: %v", c)
		}
		got[c["id"]] = c["status"] + " " + c["code"]
	}
	want := map[string]string{"memory": "warn MEMORY_BELOW_RECOMMENDED", "disk": "warn DISK_BELOW_RECOMMENDED", "container_memory": "warn ENGINE_MEMORY_LOW"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
}

// Every id and code the checks may give fits the schema of the report.
func TestMachineCodesFitTheSchema(t *testing.T) {
	for _, code := range []string{"WSL_NOT_SUPPORTED", "MEMORY_BELOW_RECOMMENDED", "MEMORY_BELOW_MINIMUM", "DISK_BELOW_RECOMMENDED",
		"DISK_BELOW_MINIMUM", "TOOL_MISSING", "ENGINE_NOT_INSTALLED", "ENGINE_NOT_RUNNING", "ENGINE_PERMISSION_DENIED",
		"ENGINE_MEMORY_LOW", "NETWORK_CUT_UNAVAILABLE"} {
		if !reCheckCode.MatchString(code) {
			t.Errorf("code %q outside the schema", code)
		}
	}
	for _, name := range requiredTools {
		if !reCheckID.MatchString("tool_" + name) {
			t.Errorf("id of %q outside the schema", name)
		}
	}
	if !reflect.DeepEqual(requiredTools, []string{"git", "curl", "jq", "make", "python3"}) {
		t.Errorf("tools %v", requiredTools)
	}
}

// An engine that never answers does not hold doctor beyond its bound, and
// an interruption during the checks stops the command without a diagnosis.
func TestMachineChecksAreBounded(t *testing.T) {
	interrupted, cancel := context.WithCancel(context.Background())
	old := host
	h := healthyHost()
	h.engine = func(ctx context.Context) poste.EngineState {
		cancel()
		<-ctx.Done()
		return poste.EngineState{State: poste.EngineStopped}
	}
	host = h
	t.Cleanup(func() { host = old })
	isolate(t, newVersionServer(t, fileOf("1.2.3")), day)
	start := time.Now()
	code, stdout, stderr := executeCtx(t, interrupted, "doctor")
	if code == exitOK || code == exitFailure || !strings.Contains(stderr, i18n.T("error.interrupted")) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "Points à corriger") || time.Since(start) > 5*time.Second {
		t.Errorf("stdout %q after %v", stdout, time.Since(start))
	}
}

func TestOneDecimal(t *testing.T) {
	for _, c := range []struct {
		bytes, unit uint64
		want        string
	}{
		{16 * gib, gib, "16"}, {7*gib - 1, gib, "6,9"}, {8162 << 20, gib, "7,9"}, {4101881856, gib, "3,8"}, {0, gib, "0"},
	} {
		if got := oneDecimal(c.bytes, c.unit); got != c.want {
			t.Errorf("oneDecimal(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}
