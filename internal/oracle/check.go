package oracle

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Codes of the oracle's own errors (contract v2, section 1).
const (
	codeNotStarted   = "ORACLE_CHECK_NOT_STARTED"
	codeTimeout      = "ORACLE_TIMEOUT"
	codeTotalTimeout = "ORACLE_TOTAL_TIMEOUT"
	codeInvalidOut   = "ORACLE_INVALID_OUTPUT"
	codeUndeclared   = "ORACLE_UNDECLARED_CODE"
	codeInterrupted  = "ORACLE_CHECK_INTERRUPTED"
)

// graceDelay is the wait for the outputs after a check is killed at its delay.
const graceDelay = 2 * time.Second

var reInteger = regexp.MustCompile(`^-?[0-9]{1,12}$`)

// errOutsideContract is never shown: it becomes ORACLE_INVALID_OUTPUT.
var errOutsideContract = errors.New(codeInvalidOut)

func outcome(c *Check, status string, code, origin *string, observed, expected any, blockedBy *string, ms int64) CheckResult {
	return CheckResult{ID: c.ID, Step: c.Step, Status: status, Code: code, Origin: origin,
		Observed: observed, Expected: expected, BlockedBy: blockedBy, DurationMS: ms}
}

func oracleError(c *Check, code string, ms int64) CheckResult {
	return outcome(c, statusError, &code, ptr(originOracle), nil, nil, nil, ms)
}

func ptr(s string) *string { return &s }

// convert checks one raw value against its whitelist entry.
func convert(raw string, spec ValueSpec) (any, error) {
	switch spec.Type {
	case typeBool:
		if raw != "true" && raw != "false" {
			return nil, errOutsideContract
		}
		return raw == "true", nil
	case typeInt, typeDuration:
		if !reInteger.MatchString(raw) {
			return nil, errOutsideContract
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < spec.Min || n > spec.Max {
			return nil, errOutsideContract
		}
		return n, nil
	}
	if !slices.Contains(spec.Values, raw) {
		return nil, errOutsideContract
	}
	return raw, nil
}

// readResult reads the SAGNAC_RESULT file: lines code=, observed=, expected=,
// each at most once. A missing file means no value.
func readResult(path string, c *Check) (code *string, observed, expected any, err error) {
	raw, readErr := os.ReadFile(path)
	values := map[string]string{}
	if readErr == nil {
		for _, line := range pySplitLines(strings.ToValidUTF8(string(raw), "�")) {
			if pyStrip(line) == "" {
				continue
			}
			key, value, found := strings.Cut(line, "=")
			_, dup := values[key]
			if !found || (key != "code" && key != "observed" && key != "expected") || dup {
				return nil, nil, nil, errOutsideContract
			}
			values[key] = value
		}
	}
	if v, ok := values["observed"]; ok {
		if observed, err = convert(v, c.Observed); err != nil {
			return nil, nil, nil, err
		}
	}
	if v, ok := values["expected"]; ok {
		if expected, err = convert(v, c.Expected); err != nil {
			return nil, nil, nil, err
		}
	}
	if v, ok := values["code"]; ok {
		code = &v
	}
	return code, observed, expected, nil
}

func elapsedMS(start time.Time) int64 { return time.Since(start).Milliseconds() }

// runCheck runs one check script in the workspace, in its own session, with
// the reduced environment of the contract (section 3, plus XDG_CONFIG_HOME on
// an empty directory; nothing of the account, no session bus), and maps its exit code and result
// file to a CheckResult. Everything the check started is killed at its end.
//
// A cancelled ctx kills the process group of the check at once, like the
// delay, and the second value is true: the outcome is not a result.
func runCheck(ctx context.Context, lab *Lab, c *Check, workspace string, seed int64, delay time.Duration, prefix []string) (CheckResult, bool) {
	start := time.Now()
	// The result file lives outside the TMPDIR of the check, which the
	// student's command inherits.
	tmp, err := os.MkdirTemp("", "sagnac-")
	if err != nil {
		return oracleError(c, codeNotStarted, elapsedMS(start)), false
	}
	defer os.RemoveAll(tmp)
	tmpResult, err := os.MkdirTemp("", "sagnac-r-")
	if err != nil {
		return oracleError(c, codeNotStarted, elapsedMS(start)), false
	}
	defer os.RemoveAll(tmpResult)
	resultFile := filepath.Join(tmpResult, "resultat")
	// An empty configuration directory of the run: tools started by the
	// check (git, a keyring helper) never read the person's own, where the
	// tokens of the CLI may be.
	config, err := os.MkdirTemp("", "sagnac-c-")
	if err != nil {
		return oracleError(c, codeNotStarted, elapsedMS(start)), false
	}
	defer os.RemoveAll(config)

	path, found := os.LookupEnv("PATH")
	if !found {
		path = "/usr/bin:/bin"
	}
	home, found := os.LookupEnv("HOME")
	if !found {
		home = tmp
	}
	argv := append(slices.Clone(prefix), c.Script)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workspace
	cmd.Env = []string{
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"LC_ALL=C", // checks never depend on the student's language
		"XDG_CONFIG_HOME=" + config,
		"SAGNAC_LAB=" + lab.ID,
		"SAGNAC_CHECK=" + c.ID,
		"SAGNAC_WORKSPACE=" + workspace,
		"SAGNAC_SEED=" + strconv.FormatInt(seed, 10),
		"SAGNAC_RESULT=" + resultFile,
		"SAGNAC_LIB=" + lab.libPath,
	}
	cmd.Env = append(cmd.Env, commonEnv(lab)...)
	// Pipes, not /dev/null: like the reference, the run lasts until the
	// check has exited and its outputs are closed, within the delay.
	outR, outW, err := os.Pipe()
	if err != nil {
		return oracleError(c, codeNotStarted, elapsedMS(start)), false
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		return oracleError(c, codeNotStarted, elapsedMS(start)), false
	}
	defer errR.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	outW.Close()
	errW.Close()
	if startErr != nil {
		return oracleError(c, codeNotStarted, elapsedMS(start)), false
	}
	pgid := cmd.Process.Pid
	exited, drained := make(chan struct{}), make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	go func() {
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, errR)
			close(done)
		}()
		_, _ = io.Copy(io.Discard, outR)
		<-done
		close(drained)
	}()
	timedOut, interrupted := false, false
	deadline := time.NewTimer(delay)
	defer deadline.Stop()
	for waitExit, waitDrain := exited, drained; (waitExit != nil || waitDrain != nil) && !timedOut; {
		select {
		case <-waitExit:
			waitExit = nil
		case <-waitDrain:
			waitDrain = nil
		case <-deadline.C:
			timedOut = true
		case <-ctx.Done():
			timedOut, interrupted = true, true
		}
	}
	if timedOut {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
		// A descendant that left the group (setsid) may still hold the
		// outputs: wait a little, then close them.
		grace := time.NewTimer(graceDelay)
		select {
		case <-drained:
		case <-grace.C:
			outR.Close()
			errR.Close()
			<-drained
		}
		grace.Stop()
	}
	// Nothing started by the check (or by the student's work) outlives it.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	rc := cmd.ProcessState.ExitCode()
	ms := elapsedMS(start)
	if interrupted {
		return oracleError(c, codeInterrupted, ms), true
	}
	if timedOut {
		return oracleError(c, codeTimeout, ms), false
	}
	return mapOutcome(c, rc, resultFile, ms), false
}

// mapOutcome maps the exit code and the result file of a finished check.
func mapOutcome(c *Check, rc int, resultFile string, ms int64) CheckResult {
	code, observed, expected, err := readResult(resultFile, c)
	if err != nil {
		return oracleError(c, codeInvalidOut, ms)
	}
	switch rc {
	case 0:
		if code != nil {
			return oracleError(c, codeInvalidOut, ms)
		}
		return outcome(c, statusPass, nil, nil, observed, expected, nil, ms)
	case 1:
		if code == nil || !slices.Contains(c.Codes, *code) {
			return oracleError(c, codeUndeclared, ms)
		}
		return outcome(c, statusFail, code, ptr(originStudent), observed, expected, nil, ms)
	case 2:
		if code == nil || !slices.Contains(c.Preconditions, *code) {
			return oracleError(c, codeUndeclared, ms)
		}
		return outcome(c, statusError, code, ptr(originPlatform), observed, expected, nil, ms)
	}
	return oracleError(c, codeInterrupted, ms)
}
