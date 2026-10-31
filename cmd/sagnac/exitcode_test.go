package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/kit"
	"github.com/giis-io/sagnac-cli/internal/oracle"
)

// Exit codes of every command (docs/contrat.md, section 5): one table maps an
// error to its code; each command is tested on every code it can reach.

func TestExitCodeTable(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"none", nil, 0},
		{"interrupted", context.Canceled, 130},
		{"interrupted wrapped", fmt.Errorf("download: %w", context.Canceled), 130},
		{"interrupted checks", oracle.ErrInterrupted, 130},
		{"usage", errUsage, 64},
		{"usage of the oracle", &oracle.UsageError{}, 64},
		{"refused", errRefused, 1},
		{"not logged in", account.ErrNotLoggedIn, 1},
		{"session ended", account.ErrSessionEnded, 1},
		{"insecure address", account.ErrInsecureURL, 1},
		{"code expired", errExpired, 1},
		{"login denied", errDenied, 1},
		{"too many devices", errTooMany, 1},
		{"lab directory exists", kit.ErrExists, 1},
		{"framework differs", kit.ErrFrameworkDiffers, 1},
		{"invalid lab", kit.ErrInvalidLab, 1},
		{"entitlement", &api.APIError{Status: 403, Code: "ENTITLEMENT_INACTIVE"}, 1},
		{"unknown lab", &api.APIError{Status: 404, Code: "UNKNOWN_LAB"}, 1},
		{"cli too old", &api.APIError{Status: 426, Code: "CLI_TOO_OLD"}, 1},
		{"unreachable", errUnreachable, 2},
		{"network", netErr, 2},
		{"timeout", context.DeadlineExceeded, 2},
		{"busy", &api.RetryError{Status: 503}, 2},
		{"server error", &account.StatusError{Status: 502}, 2},
		{"too many requests", &account.StatusError{Status: 429}, 2},
		{"api too many requests", &api.APIError{Status: 429, Code: "TOO_MANY_REQUESTS"}, 2},
		{"api 500", &api.APIError{Status: 500, Code: "INTERNAL_ERROR"}, 2},
		{"storage", errStorage, 3},
		{"keyring", account.ErrKeyringFailed, 3},
		{"no config dir", account.ErrNoConfigDir, 3},
		{"write", kit.ErrWrite, 3},
		{"checksum", kit.ErrChecksum, 3},
		{"signature", kit.ErrSignature, 3},
		{"unsafe kit", kit.ErrUnsafe, 3},
		{"bad response", api.ErrBadResponse, 3},
		{"foreign address", api.ErrForeignAddress, 3},
		{"unexpected 4xx", &account.StatusError{Status: 400}, 3},
		{"cli error code", &api.APIError{Status: 400, Code: "INVALID_VERSION"}, 3},
		{"kit of the oracle", &oracle.KitError{}, 3},
		{"anything else", errors.New("boom"), 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exitCodeOf(c.err); got != c.want {
				t.Errorf("exitCodeOf(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// executeCtx runs the command line with ctx as the context of the signals.
func executeCtx(t *testing.T, ctx context.Context, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	code = runContext(ctx, args, noEnv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func interruptedCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type codeCase struct {
	name string
	want int
	run  func(t *testing.T) (int, string, string)
}

func checkCodes(t *testing.T, cases []codeCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := c.run(t)
			if code != c.want {
				t.Errorf("code %d, want %d\nstdout %q\nstderr %q", code, c.want, stdout, stderr)
			}
		})
	}
}

func plain(args ...string) func(t *testing.T) (int, string, string) {
	return func(t *testing.T) (int, string, string) { return execute(t, args...) }
}

func interrupted(args ...string) func(t *testing.T) (int, string, string) {
	return func(t *testing.T) (int, string, string) { return executeCtx(t, interruptedCtx(), args...) }
}

func TestExitCodesHelp(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, plain("help")},
		{"64", 64, plain("help", "extra")},
		{"130", 130, interrupted("help")},
	})
}

func TestExitCodesVersion(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, plain("version")},
		{"64", 64, plain("version", "extra")},
		{"130", 130, interrupted("version")},
	})
}

func TestExitCodesDoctor(t *testing.T) {
	setup := func(t *testing.T, file string) {
		isolate(t, newVersionServer(t, file), day)
	}
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) { setup(t, fileOf("1.2.3")); return execute(t, "doctor") }},
		{"1 newer version", 1, func(t *testing.T) (int, string, string) { setup(t, fileOf("1.3.0")); return execute(t, "doctor") }},
		{"1 insecure address", 1, func(t *testing.T) (int, string, string) {
			setup(t, fileOf("1.2.3"))
			t.Setenv("SAGNAC_VERSION_URL", "http://example.com/cli/version.json")
			return execute(t, "doctor")
		}},
		{"64", 64, plain("doctor", "extra")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			setup(t, fileOf("1.2.3"))
			return executeCtx(t, interruptedCtx(), "doctor")
		}},
	})
}

func TestExitCodesPurge(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			return execute(t, "purge")
		}},
		{"3", 3, func(t *testing.T) (int, string, string) {
			blocked := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocked, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", blocked)
			return execute(t, "purge")
		}},
		{"64", 64, plain("purge", "extra")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			return executeCtx(t, interruptedCtx(), "purge")
		}},
	})
}

func TestExitCodesLogin(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) { return newRig(t, "200").run(t, "login") }},
		{"1 denied", 1, func(t *testing.T) (int, string, string) {
			return newRig(t, `{"error": "access_denied"}`).run(t, "login")
		}},
		{"1 code expired", 1, func(t *testing.T) (int, string, string) {
			return newRig(t, `{"error": "expired_token"}`).run(t, "login")
		}},
		{"2 unreachable", 2, func(t *testing.T) (int, string, string) {
			r := newRig(t, "200")
			t.Setenv("SAGNAC_API", "http://127.0.0.1:1")
			return r.run(t, "login")
		}},
		{"2 busy", 2, func(t *testing.T) (int, string, string) {
			r := newRig(t, "200")
			r.dev.codeStatus = []codeAnswer{{status: 503, retry: "1", body: `{"code": "SERVICE_OVERLOADED"}`}}
			return r.run(t, "login")
		}},
		{"3 keyring", 3, func(t *testing.T) (int, string, string) {
			r := newRig(t, "200")
			r.store = &failingStore{}
			return r.run(t, "login")
		}},
		{"64", 64, plain("login", "extra")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			newRig(t, "200")
			return executeCtx(t, interruptedCtx(), "login")
		}},
	})
}

func loggedInRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, "200")
	if err := r.store.Save(&account.Tokens{API: os.Getenv("SAGNAC_API"), Access: testAccess, Refresh: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour), Scope: "kits:read"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExitCodesLogout(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) { return loggedInRig(t).run(t, "logout") }},
		{"1 insecure address", 1, func(t *testing.T) (int, string, string) {
			r := loggedInRig(t)
			t.Setenv("SAGNAC_API", "http://example.com")
			return r.run(t, "logout")
		}},
		{"2 server error", 2, func(t *testing.T) (int, string, string) {
			r := loggedInRig(t)
			r.dev.revokeCode = http.StatusServiceUnavailable
			return r.run(t, "logout")
		}},
		{"3 unexpected answer", 3, func(t *testing.T) (int, string, string) {
			r := loggedInRig(t)
			r.dev.revokeCode = http.StatusBadRequest
			return r.run(t, "logout")
		}},
		{"64", 64, plain("logout", "extra")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			loggedInRig(t)
			return executeCtx(t, interruptedCtx(), "logout")
		}},
	})
}

func TestExitCodesPull(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) { return newPullRig(t).run(t, "pull", "hello") }},
		{"1 unknown lab", 1, func(t *testing.T) (int, string, string) { return newPullRig(t).run(t, "pull", "absent") }},
		{"1 lab directory exists", 1, func(t *testing.T) (int, string, string) {
			p := newPullRig(t)
			if err := os.Mkdir(filepath.Join(p.work, "hello"), 0o755); err != nil {
				t.Fatal(err)
			}
			return p.run(t, "pull", "hello")
		}},
		{"2 server error", 2, func(t *testing.T) (int, string, string) {
			p := newPullRig(t)
			p.kits.status, p.kits.body = http.StatusInternalServerError, `{"code": "INTERNAL_ERROR"}`
			return p.run(t, "pull", "hello")
		}},
		{"3 checksum", 3, func(t *testing.T) (int, string, string) {
			p := newPullRig(t)
			p.kits.headerSum = strings.Repeat("0", 64)
			return p.run(t, "pull", "hello")
		}},
		{"64", 64, plain("pull")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			newPullRig(t)
			return executeCtx(t, interruptedCtx(), "pull", "hello")
		}},
	})
}

func TestExitCodesOpen(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) { return newOpenRig(t).run(t, "open", "hello") }},
		{"1 unknown lab", 1, func(t *testing.T) (int, string, string) { return newOpenRig(t).run(t, "open", "absent") }},
		{"2 unreachable without copy", 2, func(t *testing.T) (int, string, string) {
			o := newOpenRig(t)
			o.srv.Close()
			return o.run(t, "open", "hello")
		}},
		{"3 cache not writable", 3, func(t *testing.T) (int, string, string) {
			o := newOpenRig(t)
			blocked := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocked, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_CACHE_HOME", blocked)
			return o.run(t, "open", "hello")
		}},
		{"64", 64, plain("open")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			newOpenRig(t)
			return executeCtx(t, interruptedCtx(), "open", "hello")
		}},
	})
}

func TestExitCodesCheck(t *testing.T) {
	checkCodes(t, []codeCase{
		{"0", 0, func(t *testing.T) (int, string, string) {
			c := newCheckRig(t, "")
			c.pass(t)
			return c.run(t, "check")
		}},
		{"1", 1, func(t *testing.T) (int, string, string) { return newCheckRig(t, "").run(t, "check") }},
		{"2", 2, func(t *testing.T) (int, string, string) {
			c := newCheckRig(t, "id: hello\nversion: 1\npreconditions:\n  - code: TOOL_MISSING\n    description: \"outil absent\"\n")
			writeCheck(t, c, "TOOL_MISSING", "#!/bin/sh\nprintf 'code=TOOL_MISSING\\n' >> \"$SAGNAC_RESULT\"\nexit 2\n", "    preconditions: [TOOL_MISSING]\n")
			return c.run(t, "check")
		}},
		{"3", 3, func(t *testing.T) (int, string, string) {
			c := newCheckRig(t, "")
			if err := os.WriteFile(filepath.Join(c.labDir, "meta.yaml"), []byte("id: hello\nversion: 0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return c.run(t, "check")
		}},
		{"64", 64, plain("check", "--unknown")},
		{"130", 130, func(t *testing.T) (int, string, string) {
			newCheckRig(t, "")
			return executeCtx(t, interruptedCtx(), "check")
		}},
	})
}

// writeCheck replaces the check of the rig by script and adds to its entry
// in meta.yaml the line extra, with a message for code.
func writeCheck(t *testing.T, c *checkRig, code, script, extra string) {
	t.Helper()
	meta := filepath.Join(c.labDir, "meta.yaml")
	raw, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, append(raw, []byte(extra)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.labDir, "checks", "verify_ok.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.labDir, "checks", "messages", "fr", code+".txt"), []byte("Outil absent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// slowServer answers path with prefix (status 200, headers of kind), then
// interrupts the command (cancel) and holds the answer until the client
// gives up: an interruption in the middle of the work. Every other path goes
// to next.
func slowServer(t *testing.T, path, contentType, prefix string, cancel context.CancelFunc, next http.Handler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("X-Sagnac-Kit-Sha256", strings.Repeat("a", 64))
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, prefix)
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	})
	mux.Handle("/", next)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func saveTokens(t *testing.T, r *rig, api string) {
	t.Helper()
	t.Setenv("SAGNAC_API", api)
	if err := r.store.Save(&account.Tokens{API: api, Access: testAccess, Refresh: testRefresh,
		ExpiresAt: time.Now().Add(time.Hour), Scope: "kits:read instructions:read"}); err != nil {
		t.Fatal(err)
	}
}

// Interrupted during the download: 130, and nothing of the kit is left in
// the directory, temporary files included.
func TestPullInterruptedDuringDownload(t *testing.T) {
	p := newPullRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := slowServer(t, "/v1/kits/hello/3", "application/gzip", "partial", cancel, p.kits)
	saveTokens(t, p.rig, api)
	code, _, stderr := executeCtx(t, ctx, "pull", "hello")
	if code != 130 {
		t.Errorf("code %d, want 130; stderr %q", code, stderr)
	}
	p.nothingWritten(t)
}

// Interrupted while the instructions arrive: 130, not the offline path, and
// no copy in the cache.
func TestOpenInterruptedDuringFetch(t *testing.T) {
	o := newOpenRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := slowServer(t, "/v1/instructions/hello", "text/html; charset=utf-8", "<html>", cancel, o.srv.Config.Handler)
	saveTokens(t, o.rig, api)
	code, _, stderr := executeCtx(t, ctx, "open", "hello")
	if code != 130 {
		t.Errorf("code %d, want 130; stderr %q", code, stderr)
	}
	if _, err := os.Stat(o.copyPath("hello-v3.html")); !os.IsNotExist(err) {
		t.Errorf("copy written: %v", err)
	}
}

// Interrupted during the revocation: 130; the local tokens are gone, as the
// person asked.
func TestLogoutInterruptedDuringRevocation(t *testing.T) {
	r := newRig(t, "200")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := slowServer(t, "/v1/revoke", "application/json", "", cancel, r.srv.Config.Handler)
	saveTokens(t, r, api)
	code, _, stderr := executeCtx(t, ctx, "logout")
	if code != 130 {
		t.Errorf("code %d, want 130; stderr %q", code, stderr)
	}
	if tokens, _ := r.store.Load(); tokens != nil {
		t.Error("tokens kept")
	}
}

// A command that has finished its work keeps its own code, even when the
// signal arrives at the very end: here while the page is handed to the
// browser, once the copy is written.
func TestFinishedCommandKeepsItsCode(t *testing.T) {
	o := newOpenRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launch = func(context.Context, string) (bool, error) {
		cancel()
		return true, nil
	}
	code, _, stderr := executeCtx(t, ctx, "open", "hello")
	if code != 0 {
		t.Errorf("code %d, want 0; stderr %q", code, stderr)
	}
	if _, err := os.Stat(o.copyPath("hello-v3.html")); err != nil {
		t.Errorf("copy missing: %v", err)
	}
}
