package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/attempt"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/instructions"
	"github.com/giis-io/sagnac-cli/internal/kit"
	"github.com/giis-io/sagnac-cli/internal/oracle"
	"github.com/giis-io/sagnac-cli/internal/queue"
)

// Replaced by the tests: the oracle, the draw of a local seed and the clock
// of the queue.
var (
	verify    = oracle.Verify
	now       = time.Now
	localSeed = func() (int64, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(1<<31))
		if err != nil {
			return 0, err
		}
		return n.Int64(), nil
	}
)

const (
	sessionLocal           = "local"
	workspaceDir           = "workspace"
	codeUnknownKit         = "UNKNOWN_KIT"
	codeInvalidSession     = "INVALID_SESSION"
	codeCLITooOld          = "CLI_TOO_OLD"
	codeIdempotencyClash   = "IDEMPOTENCY_KEY_CONFLICT"
	codeResultTooLarge     = "RESULT_TOO_LARGE"
	codeResultSchema       = "RESULT_SCHEMA_VIOLATION"
	placeholderObserved    = "{observed}"
	placeholderExpected    = "{expected}"
	messageIndent          = "     "
	checkLineCodeOpen      = " ["
	checkLineCodeClose     = "]"
	checkLineBlockedOpen   = " ("
	checkLineBlockedClose  = ")"
	jsonIndent             = "  "
	cliNamePrefix          = "sagnac/"
	labNotFoundInDirectory = "."
)

// reServerVersion is the grammar with which the server reads the field cli
// of a result (docs/contrat.md, section 4); it answers 426 CLI_TOO_OLD to a
// value it cannot read.
var reServerVersion = regexp.MustCompile(
	`^(?:[a-z0-9][a-z0-9._-]{0,31}/)?v?([0-9]{1,9})\.([0-9]{1,9})\.([0-9]{1,9})(-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// serverReadable reports whether the server can read cli as a version. Go's
// $ does not match before a final newline, like Python's fullmatch.
func serverReadable(cli string) bool { return reServerVersion.MatchString(cli) }

type checkOptions struct {
	lab     string
	json    bool
	session bool
}

// parseCheck reads "[<lab>] [--json] [--session]" in any order.
func parseCheck(args []string, stderr io.Writer) (checkOptions, bool) {
	var o checkOptions
	for _, a := range args {
		switch {
		case a == "--json":
			o.json = true
		case a == "--session":
			o.session = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
			return o, false
		case o.lab == "":
			o.lab = a
		default:
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, "check"))
			return o, false
		}
	}
	if o.lab != "" && !kit.ValidLab(o.lab) {
		fmt.Fprintln(stderr, i18n.T("check.invalid_lab", o.lab))
		return o, false
	}
	return o, true
}

// findLab looks for meta.yaml from the current directory up (the lab or its
// workspace/), like the local launcher of the kits. With a name, the lab found
// must have it; from the parent of the lab, ./<name> is taken.
func findLab(name string, stderr io.Writer) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("check.no_lab", labNotFoundInDirectory))
		return "", false
	}
	found := ""
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, "meta.yaml")); err == nil && info.Mode().IsRegular() {
			found = dir
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if name != "" && (found == "" || filepath.Base(found) != name) {
		below := filepath.Join(cwd, name)
		if info, err := os.Stat(filepath.Join(below, "meta.yaml")); err == nil && info.Mode().IsRegular() {
			return below, true
		}
		if found != "" {
			fmt.Fprintln(stderr, i18n.T("check.other_lab", filepath.Base(found), name))
			return "", false
		}
	}
	if found == "" {
		fmt.Fprintln(stderr, i18n.T("check.no_lab", cwd))
		return "", false
	}
	return found, true
}

// sendPlan says what becomes of the result once the checks are done.
type sendPlan int

const (
	planSkip  sendPlan = iota // nothing is sent: a message already said why
	planSend                  // sent now, queued if the network or the server fails
	planQueue                 // the server could not be reached: queued directly
)

// connection is what the run knows of the account before the checks.
type connection struct {
	client  *account.Client
	session *attempt.Session
	plan    sendPlan
	flush   bool // the queue of earlier runs may be sent
}

// runCheck runs the checks of the lab, shows the result, then sends it. The
// exit code is the oracle's, whatever becomes of the sending (docs/contrat.md,
// section 5).
func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, ok := parseCheck(args, stderr)
	if !ok {
		return exitCodeOf(errUsage)
	}
	oracle.CLIName = cliNamePrefix + version
	labDir, ok := findLab(o.lab, stderr)
	if !ok {
		return exitCodeOf(errUsage)
	}
	lab, err := oracle.LoadLab(labDir)
	if err != nil {
		return reportOracleError(stderr, err, labDir)
	}
	workspace := filepath.Join(lab.Dir, workspaceDir)
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		fmt.Fprintln(stderr, i18n.T("check.no_workspace", lab.Dir))
		return exitCodeOf(errUsage)
	}
	digest, err := oracle.ChecksDigest(lab.Dir)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("check.invalid_kit", "checks/"))
		return exitCodeOf(err)
	}
	// The services of the lab (its cluster) are waited for here, before the
	// session is asked for and before the ceiling of the checks starts.
	if code, stop := waitForServices(ctx, lab, stderr); stop {
		return code
	}
	// The device flow of --session prints on stdout; with --json, stdout is
	// the result alone.
	loginOut := stdout
	if o.json {
		loginOut = stderr
	}
	conn, cleanup := connect(ctx, o, lab, digest, loginOut, stderr)
	defer cleanup()
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, i18n.T("check.interrupted"))
		return exitCodeOf(ctx.Err())
	}

	seed, sessionID := int64(0), sessionLocal
	if conn.session != nil {
		seed, sessionID = conn.session.Seed, conn.session.ID
	} else if seed, err = localSeed(); err != nil {
		fmt.Fprintln(stderr, i18n.T("error.internal"))
		return exitCodeOf(errUnexpected)
	}
	noNetwork := !lab.NetworkDuringCheck
	result, err := verify(ctx, lab, workspace, seed, noNetwork)
	if errors.Is(err, oracle.ErrNetworkCutUnavailable) {
		fmt.Fprintln(stderr, i18n.T("check.network_cut_unavailable"))
		result, err = verify(ctx, lab, workspace, seed, false)
	}
	if errors.Is(err, oracle.ErrInterrupted) {
		// No verdict: the interrupted run is neither shown, nor sent, nor queued.
		fmt.Fprintln(stderr, i18n.T("check.interrupted"))
		return exitCodeOf(err)
	}
	if err != nil {
		return reportOracleError(stderr, err, lab.Dir)
	}
	result.Session = sessionID
	body, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("error.internal"))
		return exitCodeOf(errUnexpected)
	}
	info := stdout
	if o.json {
		var out bytes.Buffer
		if err := json.Indent(&out, body, "", jsonIndent); err != nil {
			fmt.Fprintln(stderr, i18n.T("error.internal"))
			return exitCodeOf(errUnexpected)
		}
		fmt.Fprintln(stdout, out.String())
		info = stderr
	} else {
		show(stdout, lab, result, workspace)
	}
	deliver(ctx, o, conn, lab, body, info, stderr)
	return result.ExitCode()
}

// connect gets the account and a session before the checks. Without
// --session, only a stored login is used: a formative check never forces a
// login. With --session, ensureTokens runs the device flow in memory. Every
// failure leaves a local check (seed of this machine, session "local").
func connect(ctx context.Context, o checkOptions, lab *oracle.Lab, digest string, stdout, stderr io.Writer) (connection, func()) {
	none := func() {}
	apiURL, err := account.URLAPI()
	if err != nil {
		reportAccountErrorFor(stderr, err, apiURL, o.session)
		fmt.Fprintln(stderr, i18n.T("check.local_only"))
		return connection{}, none
	}
	var client *account.Client
	cleanup := none
	if o.session {
		client, cleanup, err = ensureTokens(ctx, true, stdout, stderr)
		if err != nil {
			if ctx.Err() == nil {
				reportLoginError(stderr, err, apiURL, true)
				fmt.Fprintln(stderr, i18n.T("check.local_only"))
			}
			return connection{}, none
		}
	} else {
		client, err = storedLogin(ctx, apiURL, stderr)
		switch {
		case errors.Is(err, account.ErrNotLoggedIn), errors.Is(err, account.ErrSessionEnded):
			fmt.Fprintln(stderr, i18n.T("check.not_logged_in"))
			return connection{}, none
		case err != nil && attempt.Retryable(err):
			// Sent later, with the same key, once the server answers again.
			return connection{client: client, plan: planQueue}, none
		case err != nil:
			reportAccountErrorFor(stderr, err, apiURL, false)
			fmt.Fprintln(stderr, i18n.T("check.local_only"))
			return connection{}, none
		}
	}
	s, err := attempt.NewSession(ctx, client, lab.ID, lab.Version, digest)
	switch {
	case err == nil:
		return connection{client: client, session: s, plan: planSend, flush: true}, cleanup
	case ctx.Err() != nil:
		return connection{}, cleanup
	}
	if delay, ok := attempt.RateLimited(err); ok {
		// Too many sessions for this account: a local check, not queued
		// (a queued result would only meet the same limit).
		if d := delayText(delay); d != "" {
			fmt.Fprintln(stderr, i18n.T("check.rate_limited_session", d))
		} else {
			fmt.Fprintln(stderr, i18n.T("check.rate_limited_session_later"))
		}
		return connection{}, cleanup
	}
	if attempt.Retryable(err) {
		return connection{client: client, plan: planQueue}, cleanup
	}
	reportRefusal(stderr, err, lab.ID, o.session, true)
	return connection{}, cleanup
}

// storedLogin returns the client of the stored login with a valid token.
func storedLogin(ctx context.Context, apiURL string, stderr io.Writer) (*account.Client, error) {
	store, err := openStore(apiURL, false, stderr)
	if err != nil {
		return nil, err
	}
	client, err := newClient(apiURL, store)
	if err != nil {
		return nil, err
	}
	_, err = client.ValidToken(ctx)
	return client, err
}

// deliver sends the result as plan says, after the attempts left in the
// queue by earlier runs. It never changes the exit code. With --session,
// nothing is written to disk: no queue is read, none is written.
func deliver(ctx context.Context, o checkOptions, conn connection, lab *oracle.Lab, body []byte, info, stderr io.Writer) {
	if conn.plan == planSkip {
		return
	}
	if ctx.Err() != nil {
		// Interrupted after the checks: the result is shown, not sent.
		fmt.Fprintln(stderr, i18n.T("check.send_interrupted"))
		return
	}
	var q *queue.Queue
	login := ""
	if !o.session {
		var err error
		if q, err = queue.Open(); err != nil {
			q = nil
		} else if login, err = conn.client.LoginID(ctx); err != nil {
			q = nil
		} else {
			q.Now = now
		}
	}
	if q != nil && conn.flush {
		flush(ctx, q, conn.client, login, info, stderr)
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, i18n.T("check.send_interrupted"))
		return
	}
	key, err := newKey()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("check.not_recorded"))
		return
	}
	if conn.plan == planSend {
		err = attempt.Send(ctx, conn.client, key, body)
		if err == nil {
			fmt.Fprintln(info, i18n.T("check.sent"))
			return
		}
		if ctx.Err() != nil {
			// Ctrl-C during the sending: not queued (the person stopped it).
			fmt.Fprintln(stderr, i18n.T("check.send_interrupted"))
			return
		}
		if !attempt.Retryable(err) {
			reportRefusal(stderr, err, lab.ID, o.session, false)
			return
		}
		if delay, ok := attempt.RateLimited(err); ok {
			if d := delayText(delay); d != "" {
				fmt.Fprintln(stderr, i18n.T("check.rate_limited_attempt", d))
			} else {
				fmt.Fprintln(stderr, i18n.T("check.rate_limited_attempt_later"))
			}
		}
	}
	if q == nil {
		if o.session {
			fmt.Fprintln(stderr, i18n.T("check.not_sent_session"))
		} else {
			fmt.Fprintln(stderr, i18n.T("check.queue_failed"))
		}
		return
	}
	entry := queue.Entry{Key: key, API: conn.client.API, Login: login, Lab: lab.ID, Version: lab.Version, Result: body}
	if err := q.Add(entry); err != nil {
		fmt.Fprintln(stderr, i18n.T("check.queue_failed"))
		return
	}
	fmt.Fprintln(stderr, i18n.T("check.queued"))
}

// flush sends the queued attempts of this server, oldest first, each with its
// own key. A refusal (4xx other than 429) removes the entry; a network
// failure, a 5xx or a 429 stops there and keeps the rest for the next run. An entry produced under
// another connection (login) of this server is removed unsent: it may belong
// to another account.
func flush(ctx context.Context, q *queue.Queue, client *account.Client, login string, info, stderr io.Writer) {
	entries, err := q.Pending()
	if err != nil {
		return
	}
	sent, dropped := 0, 0
	for _, e := range entries {
		if e.API != client.API {
			continue
		}
		if e.Login != login {
			_ = q.Remove(e.Key)
			continue
		}
		err := attempt.Send(ctx, client, e.Key, e.Result)
		if err != nil && (attempt.Retryable(err) || ctx.Err() != nil) {
			break // kept for the next run
		}
		if q.Remove(e.Key) != nil {
			continue
		}
		if err == nil {
			sent++
		} else {
			dropped++
		}
	}
	if sent > 0 {
		fmt.Fprintln(info, i18n.T("check.queue_sent", sent))
	}
	if dropped > 0 {
		fmt.Fprintln(stderr, i18n.T("check.queue_dropped", dropped))
	}
}

// reportRefusal writes the warning of a refusal of the platform, on the
// session (before the checks) or on the attempt (after). The result is never
// queued after a refusal.
func reportRefusal(w io.Writer, err error, lab string, session, beforeChecks bool) {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		switch {
		case errors.Is(err, account.ErrNotLoggedIn), errors.Is(err, account.ErrSessionEnded):
			fmt.Fprintln(w, loginAdvice(session))
		default:
			fmt.Fprintln(w, i18n.T("error.unexpected_response"))
		}
		fmt.Fprintln(w, notSent(beforeChecks))
		return
	}
	switch apiErr.Code {
	case codeEntitlementInactive:
		switch apiErr.State {
		case "unpaid":
			fmt.Fprintln(w, i18n.T("check.entitlement_unpaid"))
		case "cancelled":
			fmt.Fprintln(w, i18n.T("check.entitlement_cancelled"))
		case "expired":
			fmt.Fprintln(w, i18n.T("check.entitlement_expired"))
		default:
			fmt.Fprintln(w, i18n.T("check.entitlement_inactive"))
		}
	case codeUnknownKit:
		if beforeChecks {
			fmt.Fprintln(w, i18n.T("check.unknown_kit", lab))
		} else {
			fmt.Fprintln(w, i18n.T("check.unknown_kit_sent"))
		}
	case codeInvalidSession:
		fmt.Fprintln(w, i18n.T("check.invalid_session"))
	case codeCLITooOld:
		if !serverReadable(oracle.CLIName) {
			fmt.Fprintln(w, i18n.T("check.dev_build"))
		} else if apiErr.Minimum != "" {
			fmt.Fprintln(w, i18n.T("check.cli_too_old", apiErr.Minimum))
		} else {
			fmt.Fprintln(w, i18n.T("check.cli_too_old_unknown"))
		}
	case codeInsufficientScope:
		if session {
			fmt.Fprintln(w, i18n.T("error.scope_session"))
		} else {
			fmt.Fprintln(w, i18n.T("check.insufficient_scope"))
		}
		fmt.Fprintln(w, notSent(beforeChecks))
	case codeInvalidToken:
		fmt.Fprintln(w, loginAdvice(session))
		fmt.Fprintln(w, notSent(beforeChecks))
	case codeIdempotencyClash, codeResultTooLarge, codeResultSchema:
		fmt.Fprintln(w, i18n.T("check.not_recorded"))
	default:
		fmt.Fprintln(w, i18n.T("error.unexpected_response"))
		fmt.Fprintln(w, notSent(beforeChecks))
	}
}

// delayText renders the delay of a Retry-After: seconds under two minutes,
// else minutes rounded up; "" when the server gave none.
func delayText(delay time.Duration) string {
	switch {
	case delay <= 0:
		return ""
	case delay < 2*time.Minute:
		return i18n.T("delay.seconds", int(delay/time.Second))
	}
	return i18n.T("delay.minutes", int((delay+time.Minute-1)/time.Minute))
}

func notSent(beforeChecks bool) string {
	if beforeChecks {
		return i18n.T("check.local_only")
	}
	return i18n.T("check.not_recorded")
}

// reportOracleError writes the message of an error of LoadLab or Verify and
// returns its exit code (exitCodeOf): 64 for a wrong use, 3 for a kit that
// breaks the contract or any other error of the oracle.
func reportOracleError(w io.Writer, err error, labDir string) int {
	var usage *oracle.UsageError
	var kitErr *oracle.KitError
	switch {
	case errors.As(err, &usage):
		fmt.Fprintln(w, i18n.T("check.no_workspace", labDir))
	case errors.As(err, &kitErr):
		fmt.Fprintln(w, i18n.T("check.invalid_kit", kitErr.Where))
	default:
		fmt.Fprintln(w, i18n.T("error.internal"))
	}
	return exitCodeOf(err)
}

// show prints the result for a person, like the reference executor does:
// one line per check, the message of the kit under a failure or an error,
// then the verdict.
func show(w io.Writer, lab *oracle.Lab, r *oracle.Result, workspace string) {
	lang := i18n.Current()
	fmt.Fprintln(w, i18n.T("check.header", lab.ID, lab.Version, len(r.Checks)))
	fmt.Fprintln(w, i18n.T("check.workspace", workspace))
	for _, c := range r.Checks {
		line := i18n.T("check.line", c.Step, c.ID, statusText(c.Status))
		if c.Code != nil {
			line += checkLineCodeOpen + *c.Code + checkLineCodeClose
		}
		if c.Status == "skip" && c.BlockedBy != nil {
			line += checkLineBlockedOpen + i18n.T("check.blocked", *c.BlockedBy) + checkLineBlockedClose
		}
		fmt.Fprintln(w, line)
		if (c.Status == "fail" || c.Status == "error") && c.Code != nil {
			text, found := lab.MessageText(*c.Code, lang)
			if !found {
				text = *c.Code
			}
			text = strings.ReplaceAll(text, placeholderObserved, valueText(c.Observed))
			text = strings.ReplaceAll(text, placeholderExpected, valueText(c.Expected))
			for _, l := range strings.Split(text, "\n") {
				fmt.Fprintln(w, messageIndent+l)
			}
		}
	}
	fmt.Fprintln(w, verdictText(r.ExitCode()))
}

func statusText(status string) string {
	switch status {
	case "pass":
		return i18n.T("check.status_pass")
	case "fail":
		return i18n.T("check.status_fail")
	case "skip":
		return i18n.T("check.status_skip")
	}
	return i18n.T("check.status_error")
}

func verdictText(code int) string {
	switch code {
	case oracle.ExitPass:
		return i18n.T("check.verdict_0")
	case oracle.ExitFail:
		return i18n.T("check.verdict_1")
	case oracle.ExitPlatform:
		return i18n.T("check.verdict_2")
	}
	return i18n.T("check.verdict_3")
}

// valueText renders an observed or expected value like the reference executor.
func valueText(v any) string {
	switch x := v.(type) {
	case nil:
		return i18n.T("check.value_none")
	case bool:
		if x {
			return i18n.T("check.value_yes")
		}
		return i18n.T("check.value_no")
	}
	return fmt.Sprint(v)
}

// newKey draws an idempotency key, a UUID v4.
func newKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// purgeLocal deletes what the device kept for a connection that ends or is
// replaced (logout, tokens refused by the server, new login): the queued
// attempts, which the next account must not send, and the offline copies of
// the instructions. The count of attempts is said on w (nil: silent) when
// there was something.
func purgeLocal(w io.Writer) {
	if q, err := queue.Open(); err == nil {
		if n, err := q.Purge(); err == nil && n > 0 && w != nil {
			fmt.Fprintln(w, i18n.T("account.queue_purged", n))
		}
	}
	_ = instructions.Purge()
}

// runPurge empties the queue of attempts not yet sent.
func runPurge(args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			fmt.Fprintln(stderr, i18n.T("error.unknown_option", a))
		} else {
			fmt.Fprintln(stderr, i18n.T("error.unexpected_argument", a, "purge"))
		}
		return exitCodeOf(errUsage)
	}
	q, err := queue.Open()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("purge.failed"))
		return exitCodeOf(errStorage)
	}
	n, err := q.Purge()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("purge.failed"))
		return exitCodeOf(errStorage)
	}
	if n == 0 {
		fmt.Fprintln(stdout, i18n.T("purge.empty"))
	} else {
		fmt.Fprintln(stdout, i18n.T("purge.done", n))
	}
	return exitOK
}
