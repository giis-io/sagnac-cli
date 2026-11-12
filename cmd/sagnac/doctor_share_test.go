package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/i18n"
)

const testReportID = "0b5f3c1e-8a4d-4e4f-9d3b-2c6a1f7e9b10"

// reportServer plays POST /v1/doctor/reports: it records what it receives
// and answers status, body and Retry-After.
type reportServer struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	status   int
	body     string
	after    string
}

func newReportServer(t *testing.T) *reportServer {
	t.Helper()
	rs := &reportServer{status: http.StatusCreated, body: `{"report_id": "` + testReportID + `"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.requests = append(rs.requests, r)
		rs.bodies = append(rs.bodies, raw)
		if rs.after != "" {
			w.Header().Set("Retry-After", rs.after)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rs.status)
		io.WriteString(w, rs.body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SAGNAC_API", srv.URL)
	return rs
}

func (rs *reportServer) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.requests)
}

// report decodes the only report received.
func (rs *reportServer) report(t *testing.T) map[string]any {
	t.Helper()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.bodies) != 1 {
		t.Fatalf("%d reports received, want 1", len(rs.bodies))
	}
	var m map[string]any
	if err := json.Unmarshal(rs.bodies[0], &m); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	return m
}

func checksOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, _ := json.Marshal(m["checks"])
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("checks: %v", err)
	}
	return out
}

// The report holds the fields of sagnac-doctor/1 and nothing else, is sent
// without token or cookie, and the id of the platform is shown.
func TestDoctorShareSendsTheSchemaOnly(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day)
	rs := newReportServer(t)
	code, stdout, stderr := execute(t, "doctor", "--share")
	if code != exitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	r := rs.requests[0]
	if r.Method != http.MethodPost || r.URL.Path != "/v1/doctor/reports" || r.URL.RawQuery != "" {
		t.Errorf("request %s %s", r.Method, r.URL.RequestURI())
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	for _, name := range []string{"Authorization", "Cookie"} {
		if r.Header.Get(name) != "" {
			t.Errorf("%s sent with the report", name)
		}
	}
	if ua := r.Header.Get("User-Agent"); ua != "sagnac/1.2.3" {
		t.Errorf("User-Agent %q", ua)
	}
	if n := len(rs.bodies[0]); n > 16<<10 {
		t.Errorf("report of %d bytes, above 16 KiB", n)
	}
	m := rs.report(t)
	want := map[string]any{
		"schema": "sagnac-doctor/1",
		"cli":    "sagnac/1.2.3",
		"os":     runtime.GOOS,
		"arch":   runtime.GOARCH,
		"checks": []any{
			map[string]any{"id": "version", "status": "ok"},
			map[string]any{"id": "support", "status": "ok"},
			map[string]any{"id": "os", "status": "ok"},
			map[string]any{"id": "memory", "status": "ok"},
			map[string]any{"id": "disk", "status": "ok"},
			map[string]any{"id": "tool_git", "status": "ok"},
			map[string]any{"id": "tool_curl", "status": "ok"},
			map[string]any{"id": "tool_jq", "status": "ok"},
			map[string]any{"id": "tool_make", "status": "ok"},
			map[string]any{"id": "tool_python3", "status": "ok"},
			map[string]any{"id": "container_engine", "status": "ok"},
			map[string]any{"id": "container_memory", "status": "ok"},
			map[string]any{"id": "network_cut", "status": "ok"},
		},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("report %v, want %v", m, want)
	}
	if !strings.Contains(stdout, i18n.T("doctor.shared", testReportID)) {
		t.Errorf("stdout lacks the report id:\n%s", stdout)
	}
	if entries, _ := os.ReadDir(os.Getenv("XDG_CONFIG_HOME")); len(entries) != 0 {
		t.Errorf("doctor --share wrote in the configuration directory: %v", entries)
	}
}

// The checks of the report follow what doctor shows; the exit code stays
// the one of doctor when the report is received.
func TestDoctorShareChecksFollowTheDiagnosis(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		status  int
		at      time.Time
		code    int
		version map[string]any
		support map[string]any
	}{
		{"newer version", fileOf("1.3.0"), 200, day, exitFailure,
			map[string]any{"id": "version", "status": "fail", "code": "NEWER_VERSION"},
			map[string]any{"id": "support", "status": "ok"}},
		{"security release", securityFile, 200, day, exitFailure,
			map[string]any{"id": "version", "status": "fail", "code": "SECURITY_RELEASE"},
			map[string]any{"id": "support", "status": "ok"}},
		{"unverified", fileOf("1.2.3"), 500, day, exitOK,
			map[string]any{"id": "version", "status": "skip", "code": "VERSION_UNVERIFIED"},
			map[string]any{"id": "support", "status": "ok"}},
		{"support ends soon", fileOf("1.2.3"), 200, time.Date(2032, 8, 1, 10, 0, 0, 0, time.UTC), exitOK,
			map[string]any{"id": "version", "status": "ok"},
			map[string]any{"id": "support", "status": "warn", "code": "SUPPORT_ENDS_SOON"}},
		{"support ended", fileOf("1.2.3"), 200, time.Date(2033, 2, 1, 10, 0, 0, 0, time.UTC), exitFailure,
			map[string]any{"id": "version", "status": "ok"},
			map[string]any{"id": "support", "status": "fail", "code": "SUPPORT_ENDED"}},
	}
	for _, c := range cases {
		vs := newVersionServer(t, c.file)
		vs.status.Store(int32(c.status))
		isolate(t, vs, c.at)
		rs := newReportServer(t)
		code, stdout, stderr := execute(t, "doctor", "--share")
		if code != c.code {
			t.Errorf("%s: code %d, stdout %q, stderr %q", c.name, code, stdout, stderr)
			continue
		}
		got := checksOf(t, rs.report(t))
		if len(got) != 13 || !reflect.DeepEqual(got[0], c.version) || !reflect.DeepEqual(got[1], c.support) {
			t.Errorf("%s: checks %v", c.name, got)
		}
	}
}

// Without --share nothing goes to the platform.
func TestDoctorWithoutShareSendsNothing(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day)
	rs := newReportServer(t)
	if code, stdout, _ := execute(t, "doctor"); code != exitOK || strings.Contains(stdout, testReportID) {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	if n := rs.count(); n != 0 {
		t.Errorf("%d requests to the platform", n)
	}
}

// What the platform answers, and the code of the command (docs/api/erreurs.md).
func TestDoctorShareAnswers(t *testing.T) {
	cases := []struct {
		name, body, after string
		status, code      int
		want              string
	}{
		{"rate limited", `{"code": "TOO_MANY_REQUESTS"}`, "60", 429, exitEnvironment,
			i18n.T("doctor.share_rate_limited", i18n.T("delay.seconds", 60))},
		{"rate limited without Retry-After", `{"code": "TOO_MANY_REQUESTS"}`, "", 429, exitEnvironment,
			i18n.T("doctor.share_rate_limited_later")},
		{"schema violation", `{"code": "REPORT_SCHEMA_VIOLATION", "errors": ["os"]}`, "", 400, exitInternal,
			i18n.T("doctor.share_refused")},
		{"too large", `{"code": "REPORT_TOO_LARGE"}`, "", 413, exitInternal, i18n.T("doctor.share_refused")},
		{"server error", `{"code": "INTERNAL_ERROR"}`, "", 500, exitEnvironment, i18n.T("doctor.share_failed")},
		{"proxy page", `<html>proxy</html>`, "", 502, exitEnvironment, i18n.T("doctor.share_failed")},
		{"route unknown", `{"code": "NOT_FOUND"}`, "", 404, exitInternal, i18n.T("error.unexpected_response")},
		{"201 without id", `{"id": 3}`, "", 201, exitInternal, i18n.T("error.unexpected_response")},
		{"201 with a foreign id", `{"report_id": "../etc"}`, "", 201, exitInternal, i18n.T("error.unexpected_response")},
	}
	for _, c := range cases {
		vs := newVersionServer(t, fileOf("1.2.3"))
		isolate(t, vs, day)
		rs := newReportServer(t)
		rs.status, rs.body, rs.after = c.status, c.body, c.after
		code, stdout, stderr := execute(t, "doctor", "--share")
		if code != c.code || !strings.Contains(stderr, c.want) || strings.Contains(stderr+stdout, "proxy") {
			t.Errorf("%s: code %d, stderr %q, want %q", c.name, code, stderr, c.want)
		}
		// The diagnosis stays shown; one request, no new try.
		if !strings.Contains(stdout, i18n.T("doctor.nothing")) || rs.count() != 1 {
			t.Errorf("%s: stdout %q, %d requests", c.name, stdout, rs.count())
		}
	}
}

func TestDoctorShareUnreachable(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day) // SAGNAC_API: a closed port
	code, _, stderr := execute(t, "doctor", "--share")
	if code != exitEnvironment || !strings.Contains(stderr, i18n.T("error.unreachable", "http://127.0.0.1:1")) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

// A binary whose version the platform cannot read sends nothing.
func TestDoctorShareDevBuildSendsNothing(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day)
	rs := newReportServer(t)
	setBuildInfo(t)
	version = "dev"
	var out, errOut strings.Builder
	code := run([]string{"doctor", "--share"}, noEnv, &out, &errOut)
	if code != exitInternal || !strings.Contains(errOut.String(), i18n.T("doctor.share_dev_build")) {
		t.Errorf("code %d, stderr %q", code, errOut.String())
	}
	if rs.count() != 0 {
		t.Errorf("%d requests", rs.count())
	}
}

func TestDoctorShareArguments(t *testing.T) {
	vs := newVersionServer(t, fileOf("1.2.3"))
	isolate(t, vs, day)
	rs := newReportServer(t)
	for _, args := range [][]string{{"doctor", "--share=yes"}, {"doctor", "--share", "now"}, {"doctor", "--json"}} {
		if code, _, _ := execute(t, args...); code != exitUsage {
			t.Errorf("%v: code %d", args, code)
		}
	}
	if rs.count() != 0 {
		t.Errorf("%d requests", rs.count())
	}
}
