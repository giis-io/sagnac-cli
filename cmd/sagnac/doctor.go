package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// clock is the current time; tests replace it.
var clock = time.Now

var reSupportMonth = regexp.MustCompile(`^([0-9]{4})-(0[1-9]|1[0-2])$`)

// supportState compares the end of support (support.yaml, injected at build
// time) with at. kind is "" far from the end, "soon" within the six months
// before the end of that month (the month included), "ended" after it.
func supportState(at time.Time) (kind, month string) {
	m := reSupportMonth.FindStringSubmatch(endOfSupport)
	if m == nil {
		return "", ""
	}
	year, _ := strconv.Atoi(m[1])
	mon, _ := strconv.Atoi(m[2])
	// First instant after the supported month.
	end := time.Date(year, time.Month(mon)+1, 1, 0, 0, 0, 0, time.UTC)
	at = at.UTC()
	switch {
	case !at.Before(end):
		return "ended", endOfSupport
	case !at.Before(end.AddDate(0, -6, 0)):
		return "soon", endOfSupport
	}
	return "", endOfSupport
}

func supportMessage(kind, month string) string {
	if kind == "ended" {
		return i18n.T("support.ended", month)
	}
	return i18n.T("support.soon", month)
}

// loadVersionFile reads the version file (daily cache, 3 s at most).
func loadVersionFile(ctx context.Context, force bool) (api.VersionStatus, error) {
	url, err := account.URLVersion()
	if err != nil {
		return api.VersionStatus{}, err
	}
	return api.LoadVersion(ctx, url, version, clock(), force), nil
}

// newerSecurityRelease: the file announces a security release that this
// binary is older than.
func newerSecurityRelease(info *api.VersionInfo) bool {
	if info == nil || !info.Security {
		return false
	}
	c, ok := api.Compare(version, info.Latest)
	return ok && c < 0
}

// noticePlan says how the warnings of a launch are made.
type noticePlan struct {
	// network: the version file may be fetched; otherwise only the cache is read.
	network bool
	// ownSupport: the command prints the end of support itself (version).
	ownSupport bool
	// ownVersions: the command reports every point itself (doctor).
	ownVersions bool
}

// startupNotices writes the warnings every launch shows on stderr: end of
// support within six months (local, no network) and a security release
// (version file). Nothing here changes an exit code or fails a command.
func startupNotices(ctx context.Context, plan noticePlan, stderr io.Writer) {
	if plan.ownVersions {
		return // doctor reports the same points in its own output
	}
	if kind, month := supportState(clock()); kind != "" && !plan.ownSupport {
		fmt.Fprintln(stderr, supportMessage(kind, month))
	}
	var st api.VersionStatus
	if plan.network {
		var err error
		if st, err = loadVersionFile(ctx, false); err != nil {
			return
		}
	} else {
		url, err := account.URLVersion()
		if err != nil {
			return
		}
		st = api.CachedVersion(url)
	}
	if newerSecurityRelease(st.Info) {
		fmt.Fprintln(stderr, i18n.T("warning.security", st.Info.Latest, st.Info.Advisory, version))
	}
}

// runDoctor reports what needs attention: a newer version, a security
// release, the end of support. It needs no account and sends no token.
// Exit code 1 when a point has to be fixed, 0 otherwise (a check that could
// not be made is not a failure); exitCodeOf gives the others.
//
// With --share, the same points go to the platform as a sagnac-doctor/1
// report (doctor_share.go); a report that does not reach it gives the code
// of that failure, the diagnosis staying shown.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	share := false
	for _, a := range args {
		switch {
		case a == "--share":
			share = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
			return exitCodeOf(errUsage)
		default:
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, "doctor"))
			return exitCodeOf(errUsage)
		}
	}
	toFix := 0
	fmt.Fprintln(stdout, i18n.T("doctor.title", version))

	st, err := loadVersionFile(ctx, true)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("error.insecure_version_url"))
		return exitCodeOf(err)
	}
	if ctx.Err() != nil {
		// Interrupted while the version file was read: nothing is reported.
		fmt.Fprintln(stderr, i18n.T("error.interrupted"))
		return exitCodeOf(ctx.Err())
	}
	versionCheck := doctorCheck{ID: "version", Status: checkSkip}
	if st.Err != nil {
		versionCheck.Code = "VERSION_UNVERIFIED"
		fmt.Fprintln(stdout, i18n.T("doctor.unverified"))
	}
	if info := st.Info; info != nil {
		switch c, ok := api.Compare(version, info.Latest); {
		case !ok:
			// A development build has no number to compare.
		case c >= 0:
			if st.Err == nil {
				versionCheck = doctorCheck{ID: "version", Status: checkOK}
				fmt.Fprintln(stdout, i18n.T("doctor.up_to_date", info.Latest))
			}
		case info.Security:
			toFix++
			versionCheck = doctorCheck{ID: "version", Status: checkFail, Code: "SECURITY_RELEASE"}
			fmt.Fprintln(stdout, i18n.T("doctor.security", info.Latest, info.Advisory, version))
		default:
			toFix++
			versionCheck = doctorCheck{ID: "version", Status: checkFail, Code: "NEWER_VERSION"}
			fmt.Fprintln(stdout, i18n.T("doctor.newer", info.Latest, version))
		}
	}

	supportCheck := doctorCheck{ID: "support", Status: checkOK}
	switch kind, month := supportState(clock()); kind {
	case "ended":
		toFix++
		supportCheck = doctorCheck{ID: "support", Status: checkFail, Code: "SUPPORT_ENDED"}
		fmt.Fprintln(stdout, i18n.T("doctor.support_ended", month))
	case "soon":
		supportCheck = doctorCheck{ID: "support", Status: checkWarn, Code: "SUPPORT_ENDS_SOON"}
		fmt.Fprintln(stdout, i18n.T("doctor.support_soon", month))
	default:
		fmt.Fprintln(stdout, i18n.T("doctor.support_ok", endOfSupport))
	}

	code := exitOK
	if toFix == 0 {
		fmt.Fprintln(stdout, i18n.T("doctor.nothing"))
	} else {
		fmt.Fprintln(stdout, i18n.T("doctor.to_fix", toFix))
		code = exitCodeOf(errRefused)
	}
	if share {
		if shared := runShare(ctx, []doctorCheck{versionCheck, supportCheck}, stdout, stderr); shared != exitOK {
			return shared
		}
	}
	return code
}
