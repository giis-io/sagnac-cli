// Command sagnac is the Sagnac command line.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// Set at build time with -ldflags -X from support.yaml (see Makefile).
var (
	version             = "dev"
	endOfSupport        = ""
	publisher           = ""
	securityContactAddr = ""
	securityPolicyURL   = ""
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			// The panic value is not shown: it could expose internal data.
			fmt.Fprintln(os.Stderr, i18n.T("error.internal"))
			os.Exit(exitCodeOf(errUnexpected))
		}
	}()
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// networkCommands may read the version file from the network (once a day, 3 s
// at most); every other launch reads the cache only.
var networkCommands = map[string]bool{
	"version": true, "purge": true, "login": true, "logout": true, "pull": true, "open": true, "check": true,
}

// run executes the command line and returns the exit code. Ctrl-C and
// SIGTERM cancel the context of every command.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runContext(ctx, args, getenv, stdout, stderr)
}

// runContext is run with the context of the signals. A command interrupted
// before the end of its work returns 130 (docs/contrat.md, section 5): before
// it starts (dispatch), or while it waits for the network (its error maps to
// 130). A command that has finished returns its own code; check keeps the
// code of the oracle once its verdict is shown.
func runContext(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	code, _ := dispatch(ctx, args, getenv, stdout, stderr)
	return code
}

// dispatch runs the command and returns its exit code and its name.
func dispatch(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) (int, string) {
	parsed := parseGlobal(args)
	// The language is chosen before any message is written, errors included.
	if err := i18n.Use(i18n.Select(parsed.lang, getenv)); err != nil {
		fmt.Fprintln(stderr, i18n.T("error.internal"))
		return exitCodeOf(errUnexpected), ""
	}
	if !buildInfoComplete() {
		fmt.Fprintln(stderr, i18n.T("error.build_info_missing"))
		return exitCodeOf(errUnexpected), ""
	}
	switch parsed.err {
	case errLangMissing:
		fmt.Fprintln(stderr, i18n.T("error.lang_missing"))
		return exitCodeOf(errUsage), ""
	case errUnknownOption:
		fmt.Fprintln(stderr, i18n.T("error.unknown_option", parsed.errArg))
		return exitCodeOf(errUsage), ""
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, i18n.T("error.interrupted"))
		return exitCodeOf(ctx.Err()), ""
	}
	command := ""
	if len(parsed.rest) > 0 {
		command = parsed.rest[0]
	}
	// Warnings of every launch, help and unknown commands included; they
	// never change the exit code. Help and unknown commands answer at once:
	// they read the cache only, never the network.
	startupNotices(ctx, noticePlan{
		network:     !parsed.help && networkCommands[command],
		ownSupport:  !parsed.help && command == "version",
		ownVersions: !parsed.help && command == "doctor",
	}, stderr)
	if parsed.help || command == "" {
		fmt.Fprint(stdout, i18n.T("usage"))
		return exitOK, command
	}
	switch command {
	case "help", "version":
		if len(parsed.rest) > 1 {
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", parsed.rest[1], command))
			return exitCodeOf(errUsage), command
		}
		if command == "help" {
			fmt.Fprint(stdout, i18n.T("usage"))
		} else {
			printVersion(stdout)
		}
		return exitOK, command
	case "doctor":
		return runDoctor(ctx, parsed.rest[1:], stdout, stderr), command
	case "purge":
		return runPurge(parsed.rest[1:], stdout, stderr), command
	case "login":
		return runLogin(ctx, parsed.rest[1:], stdout, stderr), command
	case "logout":
		return runLogout(ctx, parsed.rest[1:], stdout, stderr), command
	case "pull":
		return runPull(ctx, parsed.rest[1:], stdout, stderr), command
	case "open":
		return runOpen(ctx, parsed.rest[1:], stdout, stderr), command
	case "check":
		return runCheck(ctx, parsed.rest[1:], stdout, stderr), command
	case "clean":
		return runClean(ctx, parsed.rest[1:], stdout, stderr), command
	default:
		fmt.Fprintln(stderr, i18n.T("error.unknown_command", command))
		return exitCodeOf(errUsage), command
	}
}

// buildInfoComplete reports whether every value injected at build time is
// present. A binary built with a bare "go build" has none of them and must
// not run silently with empty fields.
func buildInfoComplete() bool {
	for _, v := range []string{version, endOfSupport, publisher, securityContactAddr, securityPolicyURL} {
		if v == "" {
			return false
		}
	}
	return true
}

type parsedArgs struct {
	lang   string
	rest   []string
	help   bool
	err    parseError
	errArg string
}

type parseError int

const (
	errNone parseError = iota
	errLangMissing
	errUnknownOption
)

// parseGlobal extracts --lang and -h/--help from args and flags any other
// option placed before the command. It builds no message: the caller does,
// once the language is known.
func parseGlobal(args []string) parsedArgs {
	var p parsedArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			p.help = true
		case a == "--lang":
			if i+1 >= len(args) {
				p.err = errLangMissing
				return p
			}
			p.lang = args[i+1]
			i++
		case strings.HasPrefix(a, "--lang="):
			p.lang = strings.TrimPrefix(a, "--lang=")
		case len(p.rest) == 0 && strings.HasPrefix(a, "-"):
			if p.err == errNone {
				p.err, p.errArg = errUnknownOption, a
			}
		default:
			p.rest = append(p.rest, a)
		}
	}
	return p
}

func printVersion(w io.Writer) {
	fmt.Fprintln(w, i18n.T("version.line", version))
	fmt.Fprintln(w, i18n.T("version.publisher", publisher))
	fmt.Fprintln(w, i18n.T("version.end_of_support", endOfSupport))
	if kind, month := supportState(clock()); kind != "" {
		fmt.Fprintln(w, supportMessage(kind, month))
	}
	fmt.Fprintln(w, i18n.T("version.security_contact", securityContactAddr))
	fmt.Fprintln(w, i18n.T("version.security_url", securityPolicyURL))
}
