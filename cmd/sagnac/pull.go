package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/kit"
)

const (
	codeEntitlementInactive = "ENTITLEMENT_INACTIVE"
	codeUnknownLab          = "UNKNOWN_LAB"
	codeInsufficientScope   = "INSUFFICIENT_SCOPE"
	codeInvalidToken        = "INVALID_TOKEN"
	codeTooManyRequests     = "TOO_MANY_REQUESTS"
	maxVersionArg           = 1 << 30
)

// lstat is replaced by the tests.
var lstat = os.Lstat

type pullOptions struct {
	lab     string
	version int // 0: the current version
	session bool
}

// parsePull reads "<lab> [--version N] [--session]" in any order.
func parsePull(args []string, stderr io.Writer) (pullOptions, bool) {
	var o pullOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--session":
			o.session = true
		case a == "--version" || strings.HasPrefix(a, "--version="):
			raw, found := strings.CutPrefix(a, "--version=")
			if !found {
				if i+1 >= len(args) {
					fmt.Fprintln(stderr, i18n.T("pull.version_invalid"))
					return o, false
				}
				i++
				raw = args[i]
			}
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > maxVersionArg {
				fmt.Fprintln(stderr, i18n.T("pull.version_invalid"))
				return o, false
			}
			o.version = n
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
			return o, false
		case o.lab == "":
			o.lab = a
		default:
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, "pull"))
			return o, false
		}
	}
	if o.lab == "" {
		fmt.Fprintln(stderr, i18n.T("pull.lab_missing"))
		return o, false
	}
	if !kit.ValidLab(o.lab) {
		fmt.Fprintln(stderr, i18n.T("pull.invalid_lab", o.lab))
		return o, false
	}
	return o, true
}

// runPull downloads the kit of a lab into the current directory: ./<lab>/
// and ./_cadre/. "make setup" is left to the person.
func runPull(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, ok := parsePull(args, stderr)
	if !ok {
		return exitCodeOf(errUsage)
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("pull.write_failed"))
		return exitCodeOf(errStorage)
	}
	// What the directory refuses is said before any connection or download.
	if code, refused := refuseDirectory(dir, o.lab, stderr); refused {
		return code
	}
	apiURL, _ := account.URLAPI()
	client, cleanup, err := ensureTokens(ctx, o.session, stdout, stderr)
	if err != nil {
		return reportLoginError(stderr, err, apiURL, o.session)
	}
	defer cleanup()
	return pull(ctx, client, dir, o, stdout, stderr)
}

func refuseDirectory(dir, lab string, stderr io.Writer) (int, bool) {
	_, err := lstat(filepath.Join(dir, lab))
	switch {
	case err == nil:
		fmt.Fprintln(stderr, i18n.T("pull.exists", lab))
		return exitCodeOf(kit.ErrExists), true
	case !errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(stderr, i18n.T("pull.stat_failed", lab))
		return exitCodeOf(errStorage), true
	}
	if err := kit.CheckFramework(dir); err != nil {
		return reportPullError(stderr, err, lab, "", false), true
	}
	return exitOK, false
}

func pull(ctx context.Context, client *account.Client, dir string, o pullOptions, stdout, stderr io.Writer) int {
	current, err := kit.Current(ctx, client, o.lab)
	if err != nil {
		return reportPullError(stderr, err, o.lab, client.API, o.session)
	}
	version, expected := current.Version, current.SHA256
	if o.version != 0 && o.version != current.Version {
		// Another version than the current one: only the header announces its digest.
		version, expected = o.version, ""
	}
	archive, err := kit.Download(ctx, client, o.lab, version, dir, expected)
	if err != nil {
		return reportPullError(stderr, err, o.lab, client.API, o.session)
	}
	defer kit.Discard(archive)
	staged, err := kit.Extract(archive, dir, o.lab)
	if err != nil {
		return reportPullError(stderr, err, o.lab, client.API, o.session)
	}
	defer staged.Remove()
	if err := staged.Install(dir); err != nil {
		return reportPullError(stderr, err, o.lab, client.API, o.session)
	}
	if staged.Reused {
		fmt.Fprintln(stdout, i18n.T("pull.framework_reused"))
	}
	labDir := filepath.Join(dir, o.lab)
	fmt.Fprintln(stdout, i18n.T("pull.done", o.lab, version, labDir))
	fmt.Fprintln(stdout, i18n.T("pull.next", labDir))
	return exitOK
}

// loginAdvice is the message of a connection that is no longer valid: with
// --session there is no login to redo, the command is simply run again.
func loginAdvice(session bool) string {
	if session {
		return i18n.T("error.session_ended")
	}
	return i18n.T("error.login_required")
}

// reportPullError writes the catalogue message of an error of pull and
// returns its exit code (exitCodeOf).
func reportPullError(w io.Writer, err error, lab, apiURL string, session bool) int {
	var apiErr *api.APIError
	switch {
	case errors.Is(err, kit.ErrExists):
		fmt.Fprintln(w, i18n.T("pull.exists", lab))
	case errors.Is(err, kit.ErrFrameworkDiffers):
		fmt.Fprintln(w, i18n.T("pull.framework_differs", lab))
	case errors.Is(err, kit.ErrChecksum):
		fmt.Fprintln(w, i18n.T("pull.checksum"))
	case errors.Is(err, kit.ErrUnsafe):
		fmt.Fprintln(w, i18n.T("pull.unsafe_kit", securityContactAddr))
	case errors.As(err, &apiErr):
		reportKitAPIError(w, apiErr, lab, session)
	case errors.Is(err, kit.ErrWrite):
		fmt.Fprintln(w, i18n.T("pull.write_failed"))
	default:
		return reportAccountErrorFor(w, err, apiURL, session)
	}
	return exitCodeOf(err)
}

func reportKitAPIError(w io.Writer, e *api.APIError, lab string, session bool) {
	switch e.Code {
	case codeEntitlementInactive:
		switch e.State {
		case "unpaid":
			fmt.Fprintln(w, i18n.T("pull.entitlement_unpaid"))
		case "cancelled":
			fmt.Fprintln(w, i18n.T("pull.entitlement_cancelled"))
		case "expired":
			fmt.Fprintln(w, i18n.T("pull.entitlement_expired"))
		default:
			fmt.Fprintln(w, i18n.T("pull.entitlement_inactive"))
		}
	case codeUnknownLab:
		fmt.Fprintln(w, i18n.T("pull.unknown_lab", lab))
	case codeInsufficientScope:
		if session {
			fmt.Fprintln(w, i18n.T("error.scope_session"))
		} else {
			fmt.Fprintln(w, i18n.T("pull.insufficient_scope"))
		}
	case codeInvalidToken:
		fmt.Fprintln(w, loginAdvice(session))
	default:
		fmt.Fprintln(w, i18n.T("error.unexpected_response"))
	}
}
