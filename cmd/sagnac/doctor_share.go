package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// The report of "sagnac doctor --share": schema sagnac-doctor/1 of the
// platform (POST /v1/doctor/reports, no account). A closed object of typed
// fields, no free text: nothing of the machine (path, user or host name)
// can go into it.
const (
	doctorSchema     = "sagnac-doctor/1"
	doctorReportPath = "/v1/doctor/reports"
	maxDoctorReport  = 16 << 10
	maxDoctorChecks  = 64
	maxShareAnswer   = 4 << 10

	codeDoctorDevBuild        = "DOCTOR_DEV_BUILD"
	codeReportTooLarge        = "REPORT_TOO_LARGE"
	codeReportSchemaViolation = "REPORT_SCHEMA_VIOLATION"
)

// Status of a check of the report.
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
	checkSkip = "skip"
)

var (
	reCheckID   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reCheckCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	reReportID  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// The values of os and arch the schema accepts.
	reportOS   = map[string]bool{"darwin": true, "linux": true, "windows": true}
	reportArch = map[string]bool{"amd64": true, "arm64": true}
)

type doctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

// doctorReport has the fields of sagnac-doctor/1. The optional field wsl is
// left out: the check "os" carries it (WSL_NOT_SUPPORTED).
type doctorReport struct {
	Schema string        `json:"schema"`
	CLI    string        `json:"cli"`
	OS     string        `json:"os"`
	Arch   string        `json:"arch"`
	Checks []doctorCheck `json:"checks"`
}

// errDevBuild: this binary cannot fill the report within the schema.
var errDevBuild = errors.New(codeDoctorDevBuild)

// newDoctorReport builds the report of checks for this binary, or
// errDevBuild when a field would be outside the schema (a version the
// platform cannot read, a system or an architecture it does not list).
func newDoctorReport(checks []doctorCheck) (*doctorReport, error) {
	r := &doctorReport{Schema: doctorSchema, CLI: cliNamePrefix + version, OS: runtime.GOOS, Arch: runtime.GOARCH, Checks: checks}
	if !serverReadable(r.CLI) || len(r.CLI) > 64 || !reportOS[r.OS] || !reportArch[r.Arch] || len(checks) > maxDoctorChecks {
		return nil, errDevBuild
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if !reCheckID.MatchString(c.ID) || seen[c.ID] || (c.Code != "" && !reCheckCode.MatchString(c.Code)) {
			return nil, errDevBuild
		}
		seen[c.ID] = true
	}
	return r, nil
}

// shareReport sends the report to the platform, without token or cookie,
// and returns the id it gives.
func shareReport(ctx context.Context, apiURL string, r *doctorReport) (string, error) {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > maxDoctorReport {
		return "", errDevBuild
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+doctorReportPath, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// A client of its own: no token store, no redirect followed.
	c := &account.Client{API: apiURL, UserAgent: "sagnac/" + version}
	c.SetUserAgent(req)
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", api.Failure(resp)
	}
	var body struct {
		ReportID string `json:"report_id"`
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxShareAnswer))
	if err != nil {
		return "", err
	}
	if json.Unmarshal(answer, &body) != nil || !reReportID.MatchString(body.ReportID) {
		return "", api.ErrBadResponse
	}
	return body.ReportID, nil
}

// runShare sends the report and says what happened; it returns 0 when the
// platform received it, else the exit code of the failure.
func runShare(ctx context.Context, checks []doctorCheck, stdout, stderr io.Writer) int {
	r, err := newDoctorReport(checks)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("doctor.share_dev_build"))
		return exitCodeOf(errUnexpected)
	}
	apiURL, err := account.URLAPI()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("error.insecure_url"))
		return exitCodeOf(err)
	}
	id, err := shareReport(ctx, apiURL, r)
	if err == nil {
		fmt.Fprintln(stdout, i18n.T("doctor.shared", id))
		return exitOK
	}
	var apiErr *api.APIError
	var status *account.StatusError
	switch {
	case errors.Is(err, errDevBuild):
		fmt.Fprintln(stderr, i18n.T("doctor.share_dev_build"))
		return exitCodeOf(errUnexpected)
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests:
		if d := delayText(apiErr.RetryAfter); d != "" {
			fmt.Fprintln(stderr, i18n.T("doctor.share_rate_limited", d))
		} else {
			fmt.Fprintln(stderr, i18n.T("doctor.share_rate_limited_later"))
		}
	case errors.As(err, &status) && status.Status == http.StatusTooManyRequests:
		fmt.Fprintln(stderr, i18n.T("doctor.share_rate_limited_later"))
	case errors.As(err, &apiErr) && (apiErr.Code == codeReportTooLarge || apiErr.Code == codeReportSchemaViolation):
		fmt.Fprintln(stderr, i18n.T("doctor.share_refused"))
	case errors.As(err, &apiErr) && apiErr.Status >= 500, errors.As(err, &status) && status.Status >= 500:
		fmt.Fprintln(stderr, i18n.T("doctor.share_failed"))
	case errors.As(err, &apiErr):
		fmt.Fprintln(stderr, i18n.T("error.unexpected_response"))
	default:
		return reportAccountErrorFor(stderr, err, apiURL, false)
	}
	return exitCodeOf(err)
}
