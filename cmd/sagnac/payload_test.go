package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/giis-io/sagnac-cli/internal/i18n"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

type shownRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    map[string]any    `json:"body"`
}

func shown(t *testing.T, stdout string) []shownRequest {
	t.Helper()
	var doc struct {
		Requests []shownRequest `json:"requests"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not the JSON of the requests alone: %v\n%s", err, stdout)
	}
	return doc.Requests
}

// --show-payload runs the checks here and contacts nothing: no session, no
// attempt, no login, no read of the version file, no queue.
func TestShowPayloadContactsNothing(t *testing.T) {
	c := newCheckRig(t, "")
	vs := newVersionServer(t, `{}`)
	t.Setenv("SAGNAC_VERSION_URL", vs.URL)
	code, stdout, stderr := c.run(t, "check", "--show-payload")
	if code != oracle.ExitFail {
		t.Fatalf("code %d, want the code of the oracle: %s", code, stderr)
	}
	if c.plat.sessionCount() != 0 || len(c.plat.sent()) != 0 || len(c.dev.codeForms) != 0 || vs.hits.Load() != 0 {
		t.Errorf("the network was used: sessions %d, attempts %d, device %d, version %d",
			c.plat.sessionCount(), len(c.plat.sent()), len(c.dev.codeForms), vs.hits.Load())
	}
	if len(c.session) != 0 {
		t.Error("the token store was opened: no token is needed to show a payload")
	}
	if len(c.pending(t)) != 0 {
		t.Error("something was queued")
	}
	if !strings.Contains(stdout, i18n.T("check.verdict_1")) {
		t.Errorf("the result is shown as usual:\n%s", stdout)
	}
	if !strings.Contains(stdout, i18n.T("check.payload_intro")) {
		t.Errorf("stdout lacks the introduction:\n%s", stdout)
	}
	for _, want := range []string{
		"POST " + c.api + "/v1/sessions", "POST " + c.api + "/v1/attempts",
		"Content-Type: application/json", "Accept: application/json", "User-Agent: sagnac/1.2.3",
		"Authorization: " + i18n.T("check.payload_token"), "Idempotency-Key: " + i18n.T("check.payload_key"),
		`"checks_sha256"`, `"schema": "sagnac-result/2"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{testAccess, testRefresh} {
		if strings.Contains(stdout+stderr, secret) {
			t.Error("a token is shown")
		}
	}
}

// What is shown is what a normal run sends: same method, address, headers
// and body, apart from what only the server or the clock decides.
func TestShowPayloadIsWhatIsSent(t *testing.T) {
	c := newCheckRig(t, "")
	if code, _, stderr := c.run(t, "check"); code != oracle.ExitFail {
		t.Fatalf("code %d: %s", code, stderr)
	}
	sentSession, sentAttempt := c.plat.sessions[0], c.plat.sent()[0]
	headers := c.plat.headers

	code, stdout, stderr := c.run(t, "check", "--show-payload", "--json")
	if code != oracle.ExitFail {
		t.Fatalf("code %d: %s", code, stderr)
	}
	reqs := shown(t, stdout)
	if len(reqs) != 2 {
		t.Fatalf("%d requests shown, want 2", len(reqs))
	}
	session, attempt := reqs[0], reqs[1]
	if session.Method != "POST" || session.URL != c.api+"/v1/sessions" ||
		attempt.Method != "POST" || attempt.URL != c.api+"/v1/attempts" {
		t.Errorf("requests: %+v %+v", session, attempt)
	}
	if !reflect.DeepEqual(session.Body, sentSession) {
		t.Errorf("session body shown %v, sent %v", session.Body, sentSession)
	}
	for _, name := range []string{"Content-Type", "Accept"} {
		for path, r := range map[string]shownRequest{"/v1/sessions": session, "/v1/attempts": attempt} {
			if r.Headers[name] != headers[path].Get(name) {
				t.Errorf("%s %s: shown %q, sent %q", path, name, r.Headers[name], headers[path].Get(name))
			}
		}
	}
	if session.Headers["User-Agent"] != headers["/v1/sessions"].Get("User-Agent") ||
		attempt.Headers["User-Agent"] != headers["/v1/attempts"].Get("User-Agent") {
		t.Error("User-Agent shown differs from the one sent")
	}
	// The names of the headers are exactly those of the real request, the
	// ones the transport adds itself apart.
	for path, r := range map[string]shownRequest{"/v1/sessions": session, "/v1/attempts": attempt} {
		for name := range headers[path] {
			switch name {
			case "Accept-Encoding", "Content-Length":
				continue
			}
			if _, ok := r.Headers[name]; !ok {
				t.Errorf("%s: header %s is sent but not shown", path, name)
			}
		}
		for name := range r.Headers {
			if _, ok := headers[path][name]; !ok {
				t.Errorf("%s: header %s is shown but not sent", path, name)
			}
		}
	}
	volatile := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			switch k {
			case "started_at", "duration_ms", "seed", "session":
				continue
			case "checks":
				var cs []any
				for _, x := range v.([]any) {
					cm := map[string]any{}
					for ck, cv := range x.(map[string]any) {
						if ck != "duration_ms" {
							cm[ck] = cv
						}
					}
					cs = append(cs, cm)
				}
				out[k] = cs
				continue
			}
			out[k] = v
		}
		return out
	}
	if !reflect.DeepEqual(volatile(attempt.Body), volatile(sentAttempt.body)) {
		t.Errorf("attempt body shown\n%v\nsent\n%v", volatile(attempt.Body), volatile(sentAttempt.body))
	}
}

func TestShowPayloadHumanBodyIsTheSentBytes(t *testing.T) {
	c := newCheckRig(t, "")
	_, stdout, _ := c.run(t, "check", "--show-payload")
	i := strings.Index(stdout, "POST "+c.api+"/v1/sessions")
	if i < 0 || !strings.Contains(stdout[i:], fmt.Sprintf(`"lab": "hello"`)) || !strings.Contains(stdout[i:], `"version": 1`) {
		t.Errorf("session body not shown:\n%s", stdout)
	}
	if !strings.Contains(stdout, i18n.T("check.payload_note")) {
		t.Errorf("the note about the server values is missing:\n%s", stdout)
	}
}

func TestShowPayloadRefusesSession(t *testing.T) {
	c := newCheckRig(t, "")
	code, _, stderr := c.run(t, "check", "--show-payload", "--session")
	if code != exitUsage || !strings.Contains(stderr, i18n.T("check.payload_with_session")) {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
	if c.plat.sessionCount() != 0 || len(c.dev.codeForms) != 0 {
		t.Error("the network was used")
	}
}

// Logged out, the payload is the same: it is what a connected run would send.
func TestShowPayloadWithoutLogin(t *testing.T) {
	c := newCheckRig(t, "")
	c.loggedOut(t)
	code, stdout, _ := c.run(t, "check", "--show-payload", "--json")
	if code != oracle.ExitFail || len(shown(t, stdout)) != 2 {
		t.Errorf("code %d\n%s", code, stdout)
	}
}
