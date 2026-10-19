package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/instructions"
	"github.com/giis-io/sagnac-cli/internal/kit"
)

// launch opens a file or a checked address; replaced by the tests.
var launch = instructions.Open

type openOptions struct {
	target  string // a lab or a module
	module  bool
	text    bool
	session bool
}

// parseOpen reads "<lab|module> [--text] [--session]" in any order.
func parseOpen(args []string, stderr io.Writer) (openOptions, bool) {
	var o openOptions
	for _, a := range args {
		switch {
		case a == "--session":
			o.session = true
		case a == "--text":
			o.text = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
			return o, false
		case o.target == "":
			o.target = a
		default:
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, "open"))
			return o, false
		}
	}
	switch {
	case o.target == "":
		fmt.Fprintln(stderr, i18n.T("open.target_missing"))
		return o, false
	case instructions.IsModule(o.target):
		o.module = true
	case !kit.ValidLab(o.target):
		fmt.Fprintln(stderr, i18n.T("open.invalid_target", o.target))
		return o, false
	}
	if o.module && o.text {
		fmt.Fprintln(stderr, i18n.T("open.text_module"))
		return o, false
	}
	return o, true
}

// runOpen opens the instructions of a lab, or loads those of a module.
func runOpen(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, ok := parseOpen(args, stderr)
	if !ok {
		return exitCodeOf(errUsage)
	}
	apiURL, _ := account.URLAPI()
	client, cleanup, err := ensureTokens(ctx, o.session, stdout, stderr)
	if err != nil {
		if unreachable(err) {
			return openUnreachable(ctx, o, 0, stdout, stderr)
		}
		return reportLoginError(stderr, err, apiURL, o.session)
	}
	defer cleanup()
	if o.module {
		return openModule(ctx, client, o, stdout, stderr)
	}
	return openLab(ctx, client, o, stdout, stderr)
}

// unreachable: the network is absent or the server does not answer. A
// server that answered, with whatever status, is reachable; an interruption
// (Ctrl-C) is not a network failure.
func unreachable(err error) bool {
	var netErr net.Error
	if errors.Is(err, context.Canceled) {
		return false
	}
	return errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded)
}

func openLab(ctx context.Context, client *account.Client, o openOptions, stdout, stderr io.Writer) int {
	version, installed := 0, false
	if dir, err := os.Getwd(); err == nil {
		version, installed = instructions.InstalledVersion(dir, o.target)
	}
	if !installed {
		current, err := kit.Current(ctx, client, o.target)
		if err != nil {
			if unreachable(err) {
				return openUnreachable(ctx, o, 0, stdout, stderr)
			}
			return reportOpenError(stderr, err, o.target, client.API, o.session)
		}
		version = current.Version
	}
	page, err := instructions.Fetch(ctx, client, o.target, version)
	var apiErr *api.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Code == instructions.CodeOfflineNotIncluded:
		return showSitePage(ctx, client.API, o, apiErr.URL, stdout, stderr)
	case err != nil && unreachable(err):
		return openUnreachable(ctx, o, version, stdout, stderr)
	case err != nil:
		return reportOpenError(stderr, err, o.target, client.API, o.session)
	}
	if o.text {
		// The text first: a cache that cannot be written does not hide it.
		fmt.Fprint(stdout, instructions.Sanitize(string(page.Text)))
		if _, _, err := instructions.Store(o.target, version, page); err != nil {
			fmt.Fprintln(stderr, i18n.T("open.cache_warning"))
		}
		return exitOK
	}
	htmlPath, _, err := instructions.Store(o.target, version, page)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("open.cache_failed"))
		return exitCodeOf(errStorage)
	}
	return launchFile(ctx, htmlPath, o.target, version, false, stdout)
}

// launchFile opens the page of the cache, or says where it is.
func launchFile(ctx context.Context, path, lab string, version int, offline bool, stdout io.Writer) int {
	if opened, err := launch(ctx, path); opened && err == nil {
		if offline {
			fmt.Fprintln(stdout, i18n.T("open.offline_opened", lab, version, path))
		} else {
			fmt.Fprintln(stdout, i18n.T("open.opened", lab, version, path))
		}
		return exitOK
	}
	fmt.Fprintln(stdout, i18n.T("open.path_only", lab, version, path))
	return exitOK
}

// showSitePage: the offer does not include the offline copy; the page of the
// site is opened, or its address shown, once checked.
func showSitePage(ctx context.Context, api string, o openOptions, raw string, stdout, stderr io.Writer) int {
	page, err := instructions.SafePageURL(api, o.target, raw)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("open.url_refused"))
		return exitCodeOf(errUnexpected)
	}
	if !o.text {
		if opened, err := launch(ctx, page); opened && err == nil {
			fmt.Fprintln(stdout, i18n.T("open.site_opened", o.target, page))
			return exitOK
		}
	}
	fmt.Fprintln(stdout, i18n.T("open.site_shown", o.target, page))
	return exitOK
}

// openUnreachable: without a network, the copy of the cache is used if there
// is one (version 0: the latest there is).
func openUnreachable(ctx context.Context, o openOptions, version int, stdout, stderr io.Writer) int {
	if o.module {
		fmt.Fprintln(stderr, i18n.T("open.offline_module"))
		return exitCodeOf(errUnreachable)
	}
	v, htmlPath, textPath, found := instructions.Local(o.target, version)
	if !found {
		fmt.Fprintln(stderr, i18n.T("open.offline_no_copy", o.target))
		return exitCodeOf(errUnreachable)
	}
	if o.text {
		if textPath == "" {
			fmt.Fprintln(stderr, i18n.T("open.offline_no_copy", o.target))
			return exitCodeOf(errUnreachable)
		}
		text, err := instructions.ReadText(textPath)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("open.cache_failed"))
			return exitCodeOf(errStorage)
		}
		fmt.Fprintln(stderr, i18n.T("open.offline_note", o.target, v))
		fmt.Fprint(stdout, text)
		return exitOK
	}
	return launchFile(ctx, htmlPath, o.target, v, true, stdout)
}

// openModule loads the instructions of every lab of a module into the cache
// without opening any of them, and lists the files.
func openModule(ctx context.Context, client *account.Client, o openOptions, stdout, stderr io.Writer) int {
	labs, err := instructions.ModuleLabs(ctx, client, o.target)
	if err != nil {
		if unreachable(err) {
			return openUnreachable(ctx, o, 0, stdout, stderr)
		}
		return reportOpenError(stderr, err, o.target, client.API, o.session)
	}
	if len(labs) == 0 {
		fmt.Fprintln(stderr, i18n.T("open.module_empty", o.target))
		return exitCodeOf(errRefused)
	}
	var lines []string
	for _, l := range labs {
		page, err := instructions.Fetch(ctx, client, l.Lab, l.Version)
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Code == instructions.CodeOfflineNotIncluded {
			fmt.Fprintln(stderr, i18n.T("open.module_not_included"))
			return exitCodeOf(apiErr)
		}
		if err != nil {
			return reportOpenError(stderr, err, l.Lab, client.API, o.session)
		}
		htmlPath, _, err := instructions.Store(l.Lab, l.Version, page)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("open.cache_failed"))
			return exitCodeOf(errStorage)
		}
		lines = append(lines, i18n.T("open.module_item", l.Lab, l.Version, htmlPath))
	}
	fmt.Fprintln(stdout, i18n.T("open.module_done", o.target, len(labs)))
	for _, l := range lines {
		fmt.Fprintln(stdout, l)
	}
	return exitOK
}

// reportOpenError writes the catalogue message of an error of open and
// returns its exit code (exitCodeOf).
func reportOpenError(w io.Writer, err error, target, apiURL string, session bool) int {
	var apiErr *api.APIError
	switch {
	case errors.As(err, &apiErr):
		switch apiErr.Code {
		case codeEntitlementInactive:
			switch apiErr.State {
			case "unpaid":
				fmt.Fprintln(w, i18n.T("open.entitlement_unpaid"))
			case "cancelled":
				fmt.Fprintln(w, i18n.T("open.entitlement_cancelled"))
			case "expired":
				fmt.Fprintln(w, i18n.T("open.entitlement_expired"))
			default:
				fmt.Fprintln(w, i18n.T("open.entitlement_inactive"))
			}
		case codeUnknownLab:
			fmt.Fprintln(w, i18n.T("open.unknown_target", target))
		case codeInsufficientScope:
			if session {
				fmt.Fprintln(w, i18n.T("error.scope_session"))
			} else {
				fmt.Fprintln(w, i18n.T("open.insufficient_scope"))
			}
		case codeInvalidToken:
			fmt.Fprintln(w, loginAdvice(session))
		default:
			fmt.Fprintln(w, i18n.T("error.unexpected_response"))
		}
	case errors.Is(err, kit.ErrInvalidLab):
		fmt.Fprintln(w, i18n.T("open.invalid_target", target))
	default:
		return reportAccountErrorFor(w, err, apiURL, session)
	}
	return exitCodeOf(err)
}
