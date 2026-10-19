package oracle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testLib is a copy of the result functions of lib/controle.sh, enough for
// the synthetic labs of these tests.
const testLib = `_sagnac_ecrire() {
    if [ -n "$2" ]; then printf 'observed=%s\n' "$2" >>"$SAGNAC_RESULT"; fi
    if [ -n "$3" ]; then printf 'expected=%s\n' "$3" >>"$SAGNAC_RESULT"; fi
    if [ -n "$1" ]; then printf 'code=%s\n' "$1" >>"$SAGNAC_RESULT"; fi
}
sagnac_reussite() { _sagnac_ecrire "" "$1" "$2"; exit 0; }
sagnac_echec() { _sagnac_ecrire "$1" "$2" "$3"; exit 1; }
sagnac_plateforme() { _sagnac_ecrire "$1" "$2" "$3"; exit 2; }
`

// fixture builds <root>/_cadre/lib/controle.sh and <root>/lab with meta.yaml,
// executable scripts under checks/ and one message per code. It returns the
// lab directory and an empty workspace.
func fixture(t *testing.T, meta string, scripts map[string]string, codes ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("_cadre/lib/controle.sh", testLib, 0o644)
	write("lab/meta.yaml", meta, 0o644)
	for name, body := range scripts {
		write("lab/checks/"+name, "#!/bin/sh\n. \"$SAGNAC_LIB\"\n"+body+"\n", 0o755)
	}
	for _, c := range codes {
		write("lab/checks/messages/fr/"+c+".txt", "message\n", 0o644)
	}
	ws := filepath.Join(root, "workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "lab"), ws
}

func mustLoad(t *testing.T, dir string) *Lab {
	t.Helper()
	lab, err := LoadLab(dir)
	if err != nil {
		t.Fatalf("LoadLab: %v", err)
	}
	return lab
}

func TestParseYAMLSubset(t *testing.T) {
	doc := `# comment
id: hello   # trailing comment
version: 1
genai: false
quoted: "a # not a comment"
single: 'it''s'
empty_map: {}
inline: [A, "b", 3, true, ~]
nothing:
checks:
  - id: verify_one
    step: 1
    codes: [X_ONE]
  -
    id: verify_two
list:
- a
- -5
`
	got, err := parseYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "hello", "version": int64(1), "genai": false, "quoted": "a # not a comment",
		"single": "it's", "empty_map": map[string]any{},
		"inline":  []any{"A", "b", int64(3), true, nil},
		"nothing": nil,
		"checks": []any{
			map[string]any{"id": "verify_one", "step": int64(1), "codes": []any{"X_ONE"}},
			map[string]any{"id": "verify_two"},
		},
		"list": []any{"a", int64(-5)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseYAML =\n%#v\nwant\n%#v", got, want)
	}
	for _, bad := range []string{
		"a: &anchor x", "a: |\n  text", "a: 1\na: 2", "a:\n\t- x", "a: {b: 1}",
		"a: [x, [y]]", "a: \"open", "---\na: 1\n---\nb: 2", "a: b: c", "  a: 1\nb: 2",
	} {
		if _, err := parseYAML(bad); err == nil {
			t.Errorf("parseYAML(%q) accepted, want an error", bad)
		}
	}
}

const okCheck = `
  - id: verify_one
    step: 1
    script: checks/one.sh
    codes: [ONE_BAD]
`

// The keys of meta.yaml form closed lists (docs/contrat.md, section 2): an
// unknown key, of contract v1 included, is a kit error that names it, and
// the five booleans read true or false only.
func TestLoadLabUnknownKeys(t *testing.T) {
	pre := "preconditions:\n  - code: NO_TOOL\n    description: \"outil absent\"\n"
	cases := []struct {
		name, meta, key string
	}{
		{"half migrated dependency", "id: x\nversion: 1\nchecks:" + okCheck + "    depend_de: [verify_one]\n", "depend_de"},
		{"half migrated delay", "id: x\nversion: 1\nchecks:" + okCheck + "    delai_s: 15\n", "delai_s"},
		{"gpu of contract v1", "id: x\nversion: 1\ngpu: non\nchecks:" + okCheck, "gpu"},
		{"docker not a boolean", "id: x\nversion: 1\ndocker: oui\nchecks:" + okCheck, "docker"},
		{"network not a boolean", "id: x\nversion: 1\nnetwork_during_check: peut-etre\nchecks:" + okCheck, "network_during_check"},
		{"top-level key of v1", "id: x\nversion: 1\nreseau_pendant_check: false\nchecks:" + okCheck, "reseau_pendant_check"},
		{"value spec key of v1", "id: x\nversion: 1\nchecks:" + okCheck + "    observed:\n      type: enum\n      valeurs: [a]\n", "valeurs"},
		{"precondition key", "id: x\nversion: 1\n" + pre + "    libelle: x\nchecks:" + okCheck, "libelle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := fixture(t, c.meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD", "NO_TOOL")
			_, err := LoadLab(dir)
			var kit *KitError
			if !errors.As(err, &kit) || !strings.Contains(kit.Reason, c.key) {
				t.Fatalf("LoadLab error = %v, want a KitError naming %q", err, c.key)
			}
		})
	}
}

func TestLoadLabRejects(t *testing.T) {
	cases := []struct {
		name  string
		meta  string
		usage bool
	}{
		{"bad id", "id: Hello\nversion: 1\nchecks:" + okCheck, false},
		{"version zero", "id: x\nversion: 0\nchecks:" + okCheck, false},
		{"genai not true/false", "id: x\nversion: 1\ngenai: peut-etre\nchecks:" + okCheck, false},
		{"genai of contract v1", "id: x\nversion: 1\ngenai: non\nchecks:" + okCheck, false},
		{"genai as an integer", "id: x\nversion: 1\ngenai: 1\nchecks:" + okCheck, false},
		{"checks of contract v1", "id: x\nversion: 1\ncontroles:" + okCheck, false},
		{"type of contract v1", "id: x\nversion: 1\nchecks:" + okCheck + "    observed:\n      type: entier\n      min: 0\n      max: 1\n", false},
		{"no checks", "id: x\nversion: 1\nchecks: []\n", false},
		{"script outside checks", "id: x\nversion: 1\nchecks:\n  - id: verify_one\n    step: 1\n    script: one.sh\n    codes: [ONE_BAD]\n", false},
		{"delay too long", "id: x\nversion: 1\nchecks:" + okCheck + "    timeout_s: 61\n", false},
		{"forward dependency", "id: x\nversion: 1\nchecks:" + okCheck + "    depends_on: [verify_two]\n", false},
		{"lowercase code", "id: x\nversion: 1\nchecks:\n  - id: verify_one\n    step: 1\n    script: checks/one.sh\n    codes: [bad]\n", false},
		{"undeclared precondition", "id: x\nversion: 1\nchecks:" + okCheck + "    preconditions: [NO_TOOL]\n", false},
		{"unbounded integer", "id: x\nversion: 1\nchecks:" + okCheck + "    observed:\n      type: integer\n", false},
		{"steps out of order", "id: x\nversion: 1\nchecks:" + okCheck +
			"  - id: verify_zero\n    step: 0\n    script: checks/one.sh\n    codes: [ZERO_BAD]\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := fixture(t, c.meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD", "ZERO_BAD", "BAD")
			_, err := LoadLab(dir)
			var kit *KitError
			if !errors.As(err, &kit) {
				t.Fatalf("LoadLab error = %v, want a KitError", err)
			}
			if kit.ExitCode() != 3 {
				t.Errorf("exit code %d, want 3", kit.ExitCode())
			}
		})
	}
	t.Run("not a lab", func(t *testing.T) {
		_, err := LoadLab(t.TempDir())
		var usage *UsageError
		if !errors.As(err, &usage) || usage.ExitCode() != 64 {
			t.Fatalf("LoadLab error = %v, want a UsageError with code 64", err)
		}
	})
	t.Run("missing message", func(t *testing.T) {
		dir, _ := fixture(t, "id: x\nversion: 1\nchecks:"+okCheck, map[string]string{"one.sh": "sagnac_reussite"})
		var kit *KitError
		if _, err := LoadLab(dir); !errors.As(err, &kit) {
			t.Fatalf("LoadLab error = %v, want a KitError", err)
		}
	})
	t.Run("script not executable", func(t *testing.T) {
		dir, _ := fixture(t, "id: x\nversion: 1\nchecks:"+okCheck, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
		if err := os.Chmod(filepath.Join(dir, "checks", "one.sh"), 0o644); err != nil {
			t.Fatal(err)
		}
		var kit *KitError
		if _, err := LoadLab(dir); !errors.As(err, &kit) {
			t.Fatalf("LoadLab error = %v, want a KitError", err)
		}
	})
}

func check(id string, step int, script string, extra string) string {
	return "  - id: " + id + "\n    step: " + strconv.Itoa(step) + "\n    script: checks/" + script + "\n" + extra
}

func str(s string) *string { return &s }

func TestVerifyOutcomes(t *testing.T) {
	enum := "    observed:\n      type: enum\n      values: [missing, present]\n    expected:\n      type: enum\n      values: [present]\n"
	cases := []struct {
		name   string
		body   string
		extra  string
		status string
		code   *string
		origin *string
		obs    any
		exit   int
	}{
		{"pass with values", "sagnac_reussite present present", "    codes: [A_BAD]\n" + enum, "pass", nil, nil, "present", 0},
		{"declared failure", "sagnac_echec A_BAD missing present", "    codes: [A_BAD]\n" + enum, "fail", str("A_BAD"), str("student"), "missing", 1},
		{"undeclared failure code", "sagnac_echec OTHER_CODE", "    codes: [A_BAD]\n", "error", str("ORACLE_UNDECLARED_CODE"), str("oracle"), nil, 3},
		{"failure without code", "exit 1", "    codes: [A_BAD]\n", "error", str("ORACLE_UNDECLARED_CODE"), str("oracle"), nil, 3},
		{"declared precondition", "sagnac_plateforme NO_TOOL", "    codes: [A_BAD]\n    preconditions: [NO_TOOL]\n", "error", str("NO_TOOL"), str("platform"), nil, 2},
		{"other exit code", "exit 7", "    codes: [A_BAD]\n", "error", str("ORACLE_CHECK_INTERRUPTED"), str("oracle"), nil, 3},
		{"value outside the list", "sagnac_echec A_BAD maybe", "    codes: [A_BAD]\n" + enum, "error", str("ORACLE_INVALID_OUTPUT"), str("oracle"), nil, 3},
		{"integer out of bounds", "sagnac_echec A_BAD 70", "    codes: [A_BAD]\n    observed:\n      type: integer\n      min: 0\n      max: 64\n", "error", str("ORACLE_INVALID_OUTPUT"), str("oracle"), nil, 3},
		{"integer in bounds", "sagnac_echec A_BAD 64", "    codes: [A_BAD]\n    observed:\n      type: integer\n      min: 0\n      max: 64\n", "fail", str("A_BAD"), str("student"), int64(64), 1},
		{"boolean default", "sagnac_reussite true", "    codes: [A_BAD]\n", "pass", nil, nil, true, 0},
		{"pass with a code", "printf 'code=A_BAD\\n' >>\"$SAGNAC_RESULT\"; exit 0", "    codes: [A_BAD]\n", "error", str("ORACLE_INVALID_OUTPUT"), str("oracle"), nil, 3},
		{"line outside contract", "printf 'texte libre\\n' >>\"$SAGNAC_RESULT\"; exit 0", "    codes: [A_BAD]\n", "error", str("ORACLE_INVALID_OUTPUT"), str("oracle"), nil, 3},
		{"duplicate key", "printf 'observed=true\\nobserved=true\\n' >>\"$SAGNAC_RESULT\"; exit 0", "    codes: [A_BAD]\n", "error", str("ORACLE_INVALID_OUTPUT"), str("oracle"), nil, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pre := ""
			if strings.Contains(c.extra, "NO_TOOL") {
				pre = "preconditions:\n  - code: NO_TOOL\n    description: \"outil absent\"\n"
			}
			meta := "id: x\nversion: 1\n" + pre + "checks:\n" + check("verify_one", 1, "one.sh", c.extra)
			dir, ws := fixture(t, meta, map[string]string{"one.sh": c.body}, "A_BAD", "NO_TOOL")
			r, err := Verify(context.Background(), mustLoad(t, dir), ws, 7, false)
			if err != nil {
				t.Fatal(err)
			}
			got := r.Checks[0]
			if got.Status != c.status || !reflect.DeepEqual(got.Code, c.code) || !reflect.DeepEqual(got.Origin, c.origin) ||
				!reflect.DeepEqual(got.Observed, c.obs) {
				t.Errorf("check = %+v (code %v, origin %v), want status %s code %v origin %v observed %v",
					got, deref(got.Code), deref(got.Origin), c.status, deref(c.code), deref(c.origin), c.obs)
			}
			if r.ExitCode() != c.exit {
				t.Errorf("exit code %d, want %d", r.ExitCode(), c.exit)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestVerifyDependenciesAndPriority(t *testing.T) {
	meta := "id: x\nversion: 1\npreconditions:\n  - code: NO_TOOL\n    description: \"outil absent\"\nchecks:\n" +
		check("verify_a", 1, "a.sh", "    codes: [A_BAD]\n") +
		check("verify_b", 2, "b.sh", "    depends_on: [verify_a]\n    codes: [B_BAD]\n") +
		check("verify_c", 3, "c.sh", "    codes: [C_BAD]\n    preconditions: [NO_TOOL]\n") +
		check("verify_d", 4, "d.sh", "    depends_on: [verify_c, verify_a]\n    codes: [D_BAD]\n")
	dir, ws := fixture(t, meta, map[string]string{
		"a.sh": "sagnac_echec A_BAD", "b.sh": "sagnac_reussite", "c.sh": "sagnac_plateforme NO_TOOL", "d.sh": "sagnac_reussite",
	}, "A_BAD", "B_BAD", "C_BAD", "D_BAD", "NO_TOOL")
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Checks {
		got = append(got, c.ID+":"+c.Status+":"+deref(c.BlockedBy))
	}
	want := []string{"verify_a:fail:<nil>", "verify_b:skip:verify_a", "verify_c:error:<nil>", "verify_d:skip:verify_c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
	if r.ExitCode() != 2 || r.Status != "error" {
		t.Errorf("status %s, exit %d; want error and 2 (platform beats student failure)", r.Status, r.ExitCode())
	}
}

func TestVerifyEnvironment(t *testing.T) {
	body := `[ "$SAGNAC_SEED" = 4242 ] || sagnac_echec ENV_BAD
[ "$LC_ALL" = C ] || sagnac_echec ENV_BAD
[ "$SAGNAC_LAB" = x ] && [ "$SAGNAC_CHECK" = verify_env ] || sagnac_echec ENV_BAD
[ "$(pwd -P)" = "$SAGNAC_WORKSPACE" ] || sagnac_echec ENV_BAD
[ -z "$SAGNAC_LEAK" ] || sagnac_echec ENV_BAD
[ -d "$TMPDIR" ] || sagnac_echec ENV_BAD
read line && sagnac_echec ENV_BAD
sagnac_reussite`
	body = "env > \"$SAGNAC_WORKSPACE/env.txt\"\n" + body
	meta := "id: x\nversion: 1\nchecks:\n" + check("verify_env", 1, "env.sh", "    codes: [ENV_BAD]\n")
	dir, ws := fixture(t, meta, map[string]string{"env.sh": body}, "ENV_BAD")
	t.Setenv("SAGNAC_LEAK", "1")
	userConfig := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userConfig)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/tmp/bus")
	t.Setenv("SAGNAC_API", "https://sagnac.example")
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 4242, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Checks[0].Status != "pass" {
		t.Errorf("environment check = %s %s, want pass", r.Checks[0].Status, deref(r.Checks[0].Code))
	}
	if r.Seed != 4242 {
		t.Errorf("seed %d, want 4242", r.Seed)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		env[k] = v
	}
	// The variables of contract v2 section 1, plus XDG_CONFIG_HOME on an empty
	// directory of the run: no SAGNAC_* of the account, no session bus, never
	// the configuration directory of the person (where the tokens may live).
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LC_ALL": true, "XDG_CONFIG_HOME": true,
		"SAGNAC_LAB": true, "SAGNAC_CHECK": true, "SAGNAC_WORKSPACE": true, "SAGNAC_SEED": true,
		"SAGNAC_RESULT": true, "SAGNAC_LIB": true, "PWD": true, "SHLVL": true, "_": true, "OLDPWD": true}
	for k := range env {
		if !allowed[k] {
			t.Errorf("variable %s reached the check", k)
		}
	}
	cfg := env["XDG_CONFIG_HOME"]
	if cfg == "" || cfg == userConfig || strings.HasPrefix(cfg, userConfig) {
		t.Errorf("XDG_CONFIG_HOME = %q, want an empty directory of the run", cfg)
	}
}

// The configuration directory given to a check is empty and removed after it.
func TestCheckConfigDirEmptyAndRemoved(t *testing.T) {
	body := `[ -d "$XDG_CONFIG_HOME" ] || sagnac_echec ENV_BAD
[ -z "$(ls -A "$XDG_CONFIG_HOME")" ] || sagnac_echec ENV_BAD
printf '%s' "$XDG_CONFIG_HOME" > "$SAGNAC_WORKSPACE/cfg"
: > "$XDG_CONFIG_HOME/left"
sagnac_reussite`
	meta := "id: x\nversion: 1\nchecks:\n" + check("verify_env", 1, "env.sh", "    codes: [ENV_BAD]\n")
	dir, ws := fixture(t, meta, map[string]string{"env.sh": body}, "ENV_BAD")
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Checks[0].Status != "pass" {
		t.Fatalf("check = %s %s, want pass", r.Checks[0].Status, deref(r.Checks[0].Code))
	}
	cfg, _ := os.ReadFile(filepath.Join(ws, "cfg"))
	if _, err := os.Stat(string(cfg)); err == nil {
		t.Errorf("configuration directory %s left after the check", cfg)
	}
}

// network_during_check: true opens the network during the checks; false, or
// nothing, keeps it cut. Any other value is a kit error (TestLoadLabUnknownKeys).
func TestNetworkDuringCheck(t *testing.T) {
	for value, want := range map[string]bool{"": false, "network_during_check: false\n": false,
		"network_during_check: true\n": true} {
		meta := "id: x\nversion: 1\n" + value + "checks:" + okCheck
		dir, _ := fixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
		if got := mustLoad(t, dir).NetworkDuringCheck; got != want {
			t.Errorf("%q: NetworkDuringCheck = %v, want %v", value, got, want)
		}
	}
}

// MessageText looks in checks/messages/<lang>, then in French, then in the
// messages of the cadre (docs/contrat.md, section 6).
func TestMessageText(t *testing.T) {
	meta := "id: x\nversion: 1\nchecks:" + okCheck
	dir, _ := fixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
	cadre := filepath.Join(filepath.Dir(dir), "_cadre", "messages", "fr")
	if err := os.MkdirAll(cadre, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cadre, "ORACLE_TIMEOUT.txt"), []byte("trop long\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lab := mustLoad(t, dir)
	for code, want := range map[string]string{"ONE_BAD": "message", "ORACLE_TIMEOUT": "trop long", "NOPE": ""} {
		got, ok := lab.MessageText(code, "en")
		if got != want || ok != (want != "") {
			t.Errorf("MessageText(%s) = %q, %v; want %q", code, got, ok, want)
		}
	}
}

func TestVerifyWorkspaceMissing(t *testing.T) {
	meta := "id: x\nversion: 1\nchecks:" + okCheck
	dir, ws := fixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
	_, err := Verify(context.Background(), mustLoad(t, dir), filepath.Join(ws, "absent"), 1, false)
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("Verify error = %v, want a UsageError", err)
	}
}

// TestResultJSONShape checks the serialised field names against schema
// sagnac-result/2: every field is present, null included.
func TestResultJSONShape(t *testing.T) {
	meta := "id: x\nversion: 3\nchecks:" + okCheck
	dir, ws := fixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 9, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var k []string
		for key := range m {
			k = append(k, key)
		}
		sort.Strings(k)
		return k
	}
	wantTop := []string{"checks", "checks_sha256", "cli", "duration_ms", "exit_code", "lab", "platform", "schema",
		"seed", "session", "started_at", "status"}
	if got := keys(top); !reflect.DeepEqual(got, wantTop) {
		t.Errorf("top-level keys %v, want %v", got, wantTop)
	}
	ctrl := top["checks"].([]any)[0].(map[string]any)
	wantCtrl := []string{"blocked_by", "code", "duration_ms", "expected", "id", "observed", "origin", "status", "step"}
	if got := keys(ctrl); !reflect.DeepEqual(got, wantCtrl) {
		t.Errorf("check keys %v, want %v", got, wantCtrl)
	}
	if ctrl["code"] != nil || ctrl["origin"] != nil || ctrl["blocked_by"] != nil {
		t.Errorf("pass check carries a code, an origin or a blocker: %v", ctrl)
	}
	plat := top["platform"].(map[string]any)
	if got := keys(plat); !reflect.DeepEqual(got, []string{"architecture", "container_engine", "engine_version", "os"}) {
		t.Errorf("platform keys %v", got)
	}
	if top["schema"] != "sagnac-result/2" || top["session"] != "local" ||
		!reflect.DeepEqual(top["lab"], map[string]any{"id": "x", "version": float64(3)}) {
		t.Errorf("unexpected header: %v", top)
	}
	if _, err := time.Parse("2006-01-02T15:04:05Z", top["started_at"].(string)); err != nil || len(top["started_at"].(string)) != 20 {
		t.Errorf("started_at %q does not match the schema pattern", top["started_at"])
	}
	if s := top["checks_sha256"].(string); len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		t.Errorf("checks_sha256 %q", s)
	}
}

// TestChecksDigest compares the digest with a direct implementation of the
// contract: meta.yaml then checks/ without mutants/ and RESULTAT.md, sorted
// path by path, executable bit included.
func TestChecksDigest(t *testing.T) {
	meta := "id: x\nversion: 1\nchecks:" + okCheck
	dir, _ := fixture(t, meta, map[string]string{"one.sh": "sagnac_reussite"}, "ONE_BAD")
	before, err := ChecksDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"checks/RESULTAT.md", "checks/mutants/m/fichiers/x", "checks/__pycache__/a.pyc", "checks/.DS_Store"} {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("ignored"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := ChecksDigest(dir); after != before {
		t.Error("excluded files changed the digest")
	}
	if err := os.Chmod(filepath.Join(dir, "checks", "messages", "fr", "ONE_BAD.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if after, _ := ChecksDigest(dir); after == before {
		t.Error("the executable bit is not part of the digest")
	}
}

// TestNetworkCut runs a check that calls curl towards the outside: under
// noNetwork it must fail. When this machine is online, the same check passes
// without the cut, so the test is not vacuous.
func TestNetworkCut(t *testing.T) {
	if _, ok := NetworkCutAvailable(); !ok {
		t.Skip("no network cut method on this machine")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	const url = "https://example.com/"
	meta := "id: x\nversion: 1\nchecks:\n" + check("verify_offline", 1, "net.sh", "    codes: [NET_REACHED]\n")
	body := "curl -s -o /dev/null --max-time 8 " + url + " && sagnac_echec NET_REACHED\nsagnac_reussite"
	dir, ws := fixture(t, meta, map[string]string{"net.sh": body}, "NET_REACHED")
	lab := mustLoad(t, dir)
	r, err := Verify(context.Background(), lab, ws, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Checks[0].Status != "pass" {
		t.Errorf("under noNetwork curl reached %s: %s %s", url, r.Checks[0].Status, deref(r.Checks[0].Code))
	}
	if exec.Command("curl", "-s", "-o", "/dev/null", "--max-time", "8", url).Run() != nil {
		t.Log("offline machine: the control run without the cut is skipped")
		return
	}
	r, err = Verify(context.Background(), lab, ws, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Checks[0].Status != "fail" || deref(r.Checks[0].Code) != "NET_REACHED" {
		t.Errorf("without the cut the check should reach %s: %s %s", url, r.Checks[0].Status, deref(r.Checks[0].Code))
	}
}

// TestTimeout: a check that never returns gives ORACLE_TIMEOUT and exit code
// 3, and nothing it started survives it.
func TestTimeout(t *testing.T) {
	meta := "id: x\nversion: 1\nchecks:\n" + check("verify_slow", 1, "slow.sh", "    timeout_s: 1\n    codes: [SLOW_BAD]\n")
	body := "sleep 300 &\necho $! > \"$SAGNAC_WORKSPACE/child.pid\"\nsleep 300\nsagnac_reussite"
	dir, ws := fixture(t, meta, map[string]string{"slow.sh": body}, "SLOW_BAD")
	start := time.Now()
	r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Verify took %s with a 1 s delay", elapsed)
	}
	c := r.Checks[0]
	if c.Status != "error" || deref(c.Code) != "ORACLE_TIMEOUT" || deref(c.Origin) != "oracle" {
		t.Errorf("check = %s %s %s, want error ORACLE_TIMEOUT oracle", c.Status, deref(c.Code), deref(c.Origin))
	}
	if r.ExitCode() != 3 {
		t.Errorf("exit code %d, want 3", r.ExitCode())
	}
	raw, err := os.ReadFile(filepath.Join(ws, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	time.Sleep(100 * time.Millisecond)
	if pid > 0 && syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("background process %d survived its check", pid)
	}
}

// escapeCommand returns a command that leaves the process group (setsid)
// and keeps the inherited standard output open for 30 s.
func escapeCommand(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("python3"); err == nil {
		return p + ` -c 'import os, time; os.setsid(); time.sleep(30)'`
	}
	if p, err := exec.LookPath("perl"); err == nil {
		return p + ` -MPOSIX -e 'POSIX::setsid(); sleep 30'`
	}
	t.Skip("neither python3 nor perl to leave the process group")
	return ""
}

func killPidFile(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// TestTimeoutEscapedDescendant: a descendant that leaves the group with
// setsid and keeps stdout open does not hold Verify beyond the delay plus
// the grace period, whether the check itself hangs or returns at once (the
// reference waits for the outputs until the delay in both cases).
func TestTimeoutEscapedDescendant(t *testing.T) {
	escape := escapeCommand(t)
	for _, tail := range []string{"sleep 30", "sagnac_reussite"} {
		t.Run(tail, func(t *testing.T) {
			meta := "id: x\nversion: 1\nchecks:\n" + check("verify_slow", 1, "slow.sh", "    timeout_s: 1\n    codes: [SLOW_BAD]\n")
			body := escape + " &\necho $! > \"$SAGNAC_WORKSPACE/pid\"\n" + tail
			dir, ws := fixture(t, meta, map[string]string{"slow.sh": body}, "SLOW_BAD")
			defer killPidFile(t, filepath.Join(ws, "pid"))
			start := time.Now()
			r, err := Verify(context.Background(), mustLoad(t, dir), ws, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if elapsed, limit := time.Since(start), time.Second+graceDelay+3*time.Second; elapsed > limit {
				t.Errorf("Verify took %s, limit %s", elapsed, limit)
			}
			if c := r.Checks[0]; deref(c.Code) != "ORACLE_TIMEOUT" || r.ExitCode() != 3 {
				t.Errorf("check = %s %s, exit %d; want ORACLE_TIMEOUT and 3", c.Status, deref(c.Code), r.ExitCode())
			}
		})
	}
}

// TestNetworkCutUnavailable: noNetwork without any method is an error,
// never a run with the network open.
func TestNetworkCutUnavailable(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	meta := "id: x\nversion: 1\nchecks:" + okCheck
	dir, ws := fixture(t, meta, map[string]string{"one.sh": ": > " + marker + "\nsagnac_reussite"}, "ONE_BAD")
	lab := mustLoad(t, dir)
	t.Setenv("PATH", t.TempDir()) // neither sandbox-exec nor unshare
	if _, ok := NetworkCutAvailable(); ok {
		t.Fatal("a network cut method is still found with an empty PATH")
	}
	r, err := Verify(context.Background(), lab, ws, 1, true)
	if !errors.Is(err, ErrNetworkCutUnavailable) || r != nil {
		t.Fatalf("Verify = %v, %v; want nil, ErrNetworkCutUnavailable", r, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a check ran although the network could not be cut")
	}
}

func TestArchitecture(t *testing.T) {
	for in, want := range map[string]string{"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64", "riscv64": "riscv64"} {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
	got := currentPlatform().Architecture
	want := ""
	if out, err := exec.Command("sysctl", "-n", "hw.optional.arm64").Output(); err == nil && strings.TrimSpace(string(out)) == "1" {
		want = "arm64"
	} else if out, err := exec.Command("uname", "-m").Output(); err == nil {
		want = normalizeArch(strings.TrimSpace(string(out)))
	}
	if want == "" || got != want {
		t.Errorf("architecture %q, machine says %q", got, want)
	}
}

// TestVerifyInterrupted: a cancelled context (Ctrl-C) kills the process
// group of the running check at once; Verify returns ErrInterrupted and no
// result, and nothing the check started survives.
func TestVerifyInterrupted(t *testing.T) {
	meta := "id: x\nversion: 1\nchecks:\n" + check("verify_slow", 1, "slow.sh", "    timeout_s: 30\n    codes: [SLOW_BAD]\n")
	body := "sleep 30 &\necho $! > \"$SAGNAC_WORKSPACE/child.pid\"\nsleep 30\nsagnac_reussite"
	dir, ws := fixture(t, meta, map[string]string{"slow.sh": body}, "SLOW_BAD")
	ctx, cancel := context.WithCancel(context.Background())
	pidFile := filepath.Join(ws, "child.pid")
	// Cancelled 200 ms after the check has started its child.
	go func() {
		for i := 0; i < 200; i++ {
			if raw, err := os.ReadFile(pidFile); err == nil && len(raw) > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	r, err := Verify(ctx, mustLoad(t, dir), ws, 1, false)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Verify returned after %s", elapsed)
	}
	if !errors.Is(err, ErrInterrupted) || r != nil {
		t.Fatalf("Verify = %v, %v; want nil, ErrInterrupted", r, err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	time.Sleep(100 * time.Millisecond)
	if pid > 0 && syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("background process %d survived the interruption", pid)
	}
}

// A context cancelled before the run starts no check.
func TestVerifyCancelledBeforeStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	meta := "id: x\nversion: 1\nchecks:" + okCheck
	dir, ws := fixture(t, meta, map[string]string{"one.sh": ": > " + marker + "\nsagnac_reussite"}, "ONE_BAD")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Verify(ctx, mustLoad(t, dir), ws, 1, false); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a check ran after the interruption")
	}
}
