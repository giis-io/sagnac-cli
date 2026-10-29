package main

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/kit"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

// Exit codes of every command (docs/contrat.md, section 5). A check run returns
// the code of the oracle (0 to 3); every other outcome goes through
// exitCodeOf, the only mapping from an error to a code.
const (
	exitOK = 0
	// exitFailure: a refusal, the person or the account has to act.
	exitFailure = 1
	// exitEnvironment: the environment does not allow the operation now
	// (network, proxy, certificate refused, time out, busy server, 5xx); for
	// check, a platform precondition. A new try may or may not succeed.
	exitEnvironment = 2
	// exitInternal: a storage error (disk, rights, keyring) or an unexpected one.
	exitInternal = 3
	// exitUsage: unknown command, option or argument.
	exitUsage = 64
	// exitInterrupted: Ctrl-C or SIGTERM before the end of the work of the
	// command (128 + SIGINT, Unix convention).
	exitInterrupted = 130
)

// Outcomes that carry no error of their own; the command has already said why.
const (
	codeExitUsage       = "EXIT_USAGE"
	codeExitRefused     = "EXIT_REFUSED"
	codeExitUnreachable = "EXIT_UNREACHABLE"
	codeExitStorage     = "EXIT_STORAGE"
	codeExitUnexpected  = "EXIT_UNEXPECTED"
)

var (
	errUsage       = errors.New(codeExitUsage)
	errRefused     = errors.New(codeExitRefused)
	errUnreachable = errors.New(codeExitUnreachable)
	errStorage     = errors.New(codeExitStorage)
	errUnexpected  = errors.New(codeExitUnexpected)
)

// refusalCodes are the codes of docs/api/erreurs.md that ask the person or
// the account to act. Any other code below 500 is an error of the CLI or a
// server it does not understand: unexpected.
var refusalCodes = map[string]bool{
	codeEntitlementInactive:             true,
	codeUnknownLab:                      true,
	codeInsufficientScope:               true,
	codeInvalidToken:                    true,
	codeCLITooOld:                       true,
	codeUnknownKit:                      true,
	codeInvalidSession:                  true,
	codeTooManyDevice:                   true,
	codeKitDownloadLimit:                true,
	"OFFLINE_INSTRUCTIONS_NOT_INCLUDED": true,
}

type exitRule struct {
	code  int
	match func(error) bool
}

// exitTable is read in order; the first rule that matches gives the code.
var exitTable = []exitRule{
	{exitInterrupted, func(err error) bool {
		return errors.Is(err, context.Canceled) || errors.Is(err, oracle.ErrInterrupted)
	}},
	{exitUsage, func(err error) bool {
		var usage *oracle.UsageError
		return errors.Is(err, errUsage) || errors.As(err, &usage)
	}},
	{exitEnvironment, serverUnreachable},
	{exitFailure, refusal},
	{exitInternal, func(error) bool { return true }},
}

// exitCodeOf is the exit code of a command that ends on err (nil: 0).
func exitCodeOf(err error) int {
	if err == nil {
		return exitOK
	}
	for _, rule := range exitTable {
		if rule.match(err) {
			return rule.code
		}
	}
	return exitInternal
}

// serverUnreachable: nothing answered, or the server says it cannot answer now.
func serverUnreachable(err error) bool {
	var netErr net.Error
	var busy *api.RetryError
	var status *account.StatusError
	var apiErr *api.APIError
	switch {
	case errors.Is(err, errUnreachable), errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &busy):
		return true
	case errors.As(err, &status):
		return status.Status >= 500 || status.Status == http.StatusTooManyRequests
	case errors.As(err, &apiErr):
		// A code that asks the account to act stays a refusal whatever its
		// status (KIT_DOWNLOAD_LIMIT is a 429).
		return !refusalCodes[apiErr.Code] && (apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests)
	}
	return false
}

// refusal: the person or the account has to act before trying again.
func refusal(err error) bool {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return refusalCodes[apiErr.Code]
	}
	for _, target := range []error{errRefused, account.ErrNotLoggedIn, account.ErrSessionEnded, account.ErrInsecureURL,
		errExpired, errDenied, errTooMany, kit.ErrExists, kit.ErrFrameworkDiffers, kit.ErrInvalidLab} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
