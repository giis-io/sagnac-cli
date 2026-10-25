package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/attempt"
	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

const showPayloadOption = "--show-payload"

// showPayload shows the two requests a connected "sagnac check" would send
// (POST /v1/sessions before the checks, POST /v1/attempts after), built by the
// same code that sends them, and sends nothing. The checks have run here, with
// a local seed, so the attempt body is the real result; the session and the
// seed of a connected run come from the server. The token is never shown.
// The exit code is the one of the oracle, as for any check.
func showPayload(o checkOptions, lab *oracle.Lab, digest string, result *oracle.Result, body []byte,
	workspace string, stdout, stderr io.Writer) int {
	api, err := account.URLAPI()
	if err != nil {
		// The address is not usable: show the paths alone, as a connected run
		// would refuse before sending anything.
		reportAccountErrorFor(stderr, err, "", false)
		api = ""
	}
	sessionBody, err := attempt.SessionBody(lab.ID, lab.Version, digest)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("error.internal"))
		return exitCodeOf(errUnexpected)
	}
	ua := cliNamePrefix + version
	ph := attempt.Placeholders{Authorization: i18n.T("check.payload_token"), Key: i18n.T("check.payload_key")}
	requests := []attempt.Request{
		attempt.Describe(api, attempt.SessionsPath, ua, sessionBody, ph),
		attempt.Describe(api, attempt.AttemptsPath, ua, body, ph),
	}
	if o.json {
		out, err := json.MarshalIndent(map[string]any{"requests": requests}, "", jsonIndent)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("error.internal"))
			return exitCodeOf(errUnexpected)
		}
		fmt.Fprintln(stdout, string(out))
		return result.ExitCode()
	}
	show(stdout, lab, result, workspace)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, i18n.T("check.payload_intro"))
	for i, r := range requests {
		fmt.Fprintln(stdout)
		if i == 0 {
			fmt.Fprintln(stdout, i18n.T("check.payload_session", r.Method, r.URL))
		} else {
			fmt.Fprintln(stdout, i18n.T("check.payload_attempt", r.Method, r.URL))
		}
		names := make([]string, 0, len(r.Headers))
		for name := range r.Headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintln(stdout, i18n.T("check.payload_header", name, r.Headers[name]))
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, r.Body, messageIndent, jsonIndent); err != nil {
			fmt.Fprintln(stderr, i18n.T("error.internal"))
			return exitCodeOf(errUnexpected)
		}
		fmt.Fprintln(stdout, i18n.T("check.payload_body", len(r.Body)))
		fmt.Fprintln(stdout, messageIndent+pretty.String())
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, i18n.T("check.payload_note"))
	return result.ExitCode()
}
