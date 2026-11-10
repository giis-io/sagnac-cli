package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/giis-io/sagnac-cli/internal/i18n"
)

const (
	securityURL     = "https://github.com/giis-io/sagnac-cli/blob/main/SECURITY.md"
	securityContact = "securite@sagnac.fr"
	legalNoticeURL  = "https://sagnac.fr/mentions-legales"
	apacheSHA256    = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"
)

func noEnv(string) string { return "" }

func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	code = run(args, noEnv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func setBuildInfo(t *testing.T) {
	t.Helper()
	oldV, oldE, oldP, oldC, oldU := version, endOfSupport, publisher, securityContactAddr, securityPolicyURL
	version, endOfSupport, publisher = "1.2.3", "2032-12", "GIIS"
	securityContactAddr, securityPolicyURL = securityContact, securityURL
	t.Cleanup(func() {
		version, endOfSupport, publisher, securityContactAddr, securityPolicyURL = oldV, oldE, oldP, oldC, oldU
	})
}

func TestVersionShown(t *testing.T) {
	code, stdout, stderr := execute(t, "version")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	for _, want := range []string{"1.2.3", "2032-12", "GIIS", securityContact, securityURL} {
		if !strings.Contains(stdout, want) {
			t.Errorf("version output lacks %q:\n%s", want, stdout)
		}
	}
	for _, forbidden := range []string{"SASU", "Champs"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("version output must not contain %q:\n%s", forbidden, stdout)
		}
	}
}

func TestNoticePresent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "NOTICE"))
	if err != nil {
		t.Fatalf("NOTICE missing: %v", err)
	}
	notice := string(data)
	for _, want := range []string{"GIIS", legalNoticeURL, securityContact} {
		if !strings.Contains(notice, want) {
			t.Errorf("NOTICE lacks %q", want)
		}
	}
	// The postal address lives in the legal notice of the site, not in the repository.
	if strings.Contains(notice, "Champs") {
		t.Errorf("NOTICE must not carry the postal address")
	}
}

func TestApacheLicense(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "LICENSE"))
	if err != nil {
		t.Fatalf("LICENSE missing: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if !strings.HasPrefix(text, "Apache License\n") || !strings.Contains(text, "Version 2.0, January 2004") {
		t.Errorf("LICENSE does not start with the Apache 2.0 header")
	}
	if !strings.Contains(text, "END OF TERMS AND CONDITIONS") {
		t.Errorf("LICENSE lacks the end of terms")
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != apacheSHA256 {
		t.Errorf("LICENSE sha256 = %s, want the official text (%s)", got, apacheSHA256)
	}
}

func TestSupportFileComplete(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "support.yaml"))
	if err != nil {
		t.Fatalf("support.yaml missing: %v", err)
	}
	for _, key := range []string{"end_of_support", "publisher", "security_contact", "security_url"} {
		re := regexp.MustCompile(`(?m)^` + key + `:[ \t]*\S+`)
		if !re.Match(data) {
			t.Errorf("support.yaml lacks a value for %q", key)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	code, stdout, stderr := execute(t, "frobnicate")
	if code != exitUsage || exitUsage != 64 {
		t.Errorf("exit code = %d, want 64", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty, got %q", stdout)
	}
	if !strings.Contains(stderr, "frobnicate") {
		t.Errorf("stderr should name the command, got %q", stderr)
	}
}

func TestUnknownOptionAndMissingLangValue(t *testing.T) {
	if code, _, _ := execute(t, "--nope", "version"); code != exitUsage {
		t.Errorf("unknown option: exit code = %d, want 64", code)
	}
	if code, _, _ := execute(t, "version", "--lang"); code != exitUsage {
		t.Errorf("--lang without value: exit code = %d, want 64", code)
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"-h"}, {"--help"}, {"version", "--help"}} {
		code, stdout, _ := execute(t, args...)
		if code != exitOK || !strings.Contains(stdout, "version") {
			t.Errorf("args %v: code %d, output %q", args, code, stdout)
		}
	}
}

func TestExtraArgument(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"help", "extra"}} {
		code, stdout, stderr := execute(t, args...)
		if code != exitUsage || stdout != "" || !strings.Contains(stderr, "extra") {
			t.Errorf("args %v: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestBuildInfoRequired(t *testing.T) {
	fields := map[string]*string{
		"version": &version, "endOfSupport": &endOfSupport, "publisher": &publisher,
		"securityContactAddr": &securityContactAddr, "securityPolicyURL": &securityPolicyURL,
	}
	for name, field := range fields {
		setBuildInfo(t)
		*field = ""
		for _, args := range [][]string{{"version"}, {"help"}, {}} {
			var out, errOut bytes.Buffer
			code := run(args, noEnv, &out, &errOut)
			if code != exitInternal || out.Len() != 0 || errOut.Len() == 0 {
				t.Errorf("empty %s, args %v: code %d, stdout %q, stderr %q", name, args, code, out.String(), errOut.String())
			}
		}
	}
}

func TestErrorUsesChosenLanguage(t *testing.T) {
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	env := func(k string) string {
		if k == "LANG" {
			return "fr_FR.UTF-8"
		}
		return ""
	}
	// The error message is produced after the language is chosen.
	if code := run([]string{"--lang", "fr", "--bad"}, env, &out, &errOut); code != exitUsage {
		t.Fatalf("code %d", code)
	}
	if got := i18n.Current(); got != "fr" || !strings.Contains(errOut.String(), "--bad") {
		t.Errorf("language %q, stderr %q", got, errOut.String())
	}
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(data)
}

func supportValue(t *testing.T, key string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + key + `:[ \t]*(\S+)`).FindStringSubmatch(readRepoFile(t, "support.yaml"))
	if m == nil {
		t.Fatalf("support.yaml lacks %q", key)
	}
	return m[1]
}

var frenchMonths = []string{"janvier", "février", "mars", "avril", "mai", "juin",
	"juillet", "août", "septembre", "octobre", "novembre", "décembre"}

// TestSupportDateInSecurityPolicy checks that SECURITY.md states the support
// end of support.yaml as "<month in letters> <year>".
func TestSupportDateInSecurityPolicy(t *testing.T) {
	var year, month int
	if _, err := fmt.Sscanf(supportValue(t, "end_of_support"), "%d-%d", &year, &month); err != nil || month < 1 || month > 12 {
		t.Fatalf("end_of_support is not YYYY-MM: %v", err)
	}
	want := fmt.Sprintf("%s %d", frenchMonths[month-1], year)
	if !strings.Contains(readRepoFile(t, "SECURITY.md"), want) {
		t.Errorf("SECURITY.md does not state the end of support as %q", want)
	}
}

// TestBuiltBinaryShowsSupportValues builds the binary with the Makefile and
// checks that it shows the values of support.yaml.
func TestBuiltBinaryShowsSupportValues(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	bin := filepath.Join(t.TempDir(), "sagnac")
	build := exec.Command("make", "build", "BIN="+bin)
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("running the built binary: %v", err)
	}
	// The version line is readable by the server once prefixed with "sagnac/".
	first, _, _ := strings.Cut(string(out), "\n")
	v := strings.TrimPrefix(first, "sagnac ")
	if !serverReadable(cliNamePrefix+v) || !strings.HasPrefix(strings.TrimPrefix(v, "v"), strings.TrimSpace(readRepoFile(t, "VERSION"))) {
		t.Errorf("built version %q is not readable by the server", v)
	}
	for _, key := range []string{"end_of_support", "publisher", "security_contact", "security_url"} {
		if want := supportValue(t, key); !strings.Contains(string(out), want) {
			t.Errorf("version output lacks %s value %q:\n%s", key, want, out)
		}
	}
}

func TestBareGoBuildRefuses(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	bin := filepath.Join(t.TempDir(), "bare")
	build := exec.Command("go", "build", "-o", bin, "./cmd/sagnac")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != exitInternal || stdout.Len() != 0 {
		t.Errorf("bare binary: err %v, stdout %q", err, stdout.String())
	}
}

// serverReadable follows the grammar of the server for the field cli.
func TestServerVersionPattern(t *testing.T) {
	for v, want := range map[string]bool{"sagnac/0.1.0": true, "sagnac/0.1.0+27f6614-dirty": true, "sagnac/v1.2.3": true,
		"sagnac/0.1.0-rc.1": true, "sagnac/27f6614-dirty": false, "sagnac/dev": false, "sagnac/0.1": false,
		"sagnac/0.1.0\n": false, "Sagnac/0.1.0": false} {
		if got := serverReadable(v); got != want {
			t.Errorf("serverReadable(%q) = %v", v, got)
		}
	}
	raw := strings.TrimSpace(readRepoFile(t, "VERSION"))
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(raw) || !serverReadable(cliNamePrefix+raw) {
		t.Errorf("VERSION = %q", raw)
	}
}

func TestExitCodesContract(t *testing.T) {
	if exitInterrupted != 130 {
		t.Errorf("interrupted exit code %d, want 130 (docs/contrat.md)", exitInterrupted)
	}
	if exitOK != 0 || exitFailure != 1 || exitEnvironment != 2 || exitInternal != 3 || exitUsage != 64 {
		t.Errorf("exit codes differ from docs/contrat.md: %d %d %d %d %d", exitOK, exitFailure, exitEnvironment, exitInternal, exitUsage)
	}
}

func TestLanguageChoice(t *testing.T) {
	setBuildInfo(t)
	var out, errOut bytes.Buffer
	env := func(k string) string {
		if k == "LANG" {
			return "de_DE.UTF-8"
		}
		return ""
	}
	if code := run([]string{"--lang", "en", "version"}, env, &out, &errOut); code != exitOK {
		t.Fatalf("unknown language must fall back, exit code = %d (%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "GIIS") {
		t.Errorf("fallback output lacks content: %q", out.String())
	}
	if got := i18n.Current(); got != "fr" {
		t.Errorf("current language = %q, want fr", got)
	}
}

// textCall reports whether pkg.name writes or builds text: fmt print, format
// and Errorf functions, errors.New, io.WriteString, and any WriteString method
// (os.Stderr.WriteString, os.Stdout.WriteString, buffers).
func textCall(pkg, name string) bool {
	switch {
	case pkg == "fmt":
		return strings.Contains(name, "print") || strings.Contains(name, "Print") || name == "Errorf"
	case pkg == "errors":
		return name == "New"
	case pkg == "io":
		return name == "WriteString"
	}
	return name == "WriteString"
}

// TestNoTextOutsideCatalog parses the sources of the CLI and checks that every
// i18n.T call uses a literal key present in fr.json, and that no user-facing
// string literal is passed directly to a text-producing call (see textCall).
// Known limit: a literal first stored in a variable or a constant and passed
// by name is not seen.
func TestNoTextOutsideCatalog(t *testing.T) {
	raw := readRepoFile(t, filepath.Join("internal", "i18n", "fr.json"))
	catalog := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []string
	for _, dir := range []string{".", filepath.Join("..", "..", "internal")} {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			// internal/i18n never shows its errors: run prints a catalogue message instead.
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") &&
				!strings.Contains(filepath.ToSlash(p), "internal/i18n/") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Fatal("no source files found")
	}
	used := 0
	letters := regexp.MustCompile(`[A-Za-zÀ-ÿ]`)
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
	for _, f := range files {
		tree, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "T" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "i18n" {
					if len(call.Args) == 0 {
						t.Errorf("%s: i18n.T without key", fset.Position(call.Pos()))
						return true
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: i18n.T needs a literal key", fset.Position(call.Pos()))
						return true
					}
					key, _ := strconv.Unquote(lit.Value)
					used++
					if _, found := catalog[key]; !found {
						t.Errorf("%s: key %q missing from fr.json", fset.Position(call.Pos()), key)
					}
					return true
				}
			}
			pkgName := ""
			switch x := sel.X.(type) {
			case *ast.Ident:
				pkgName = x.Name
			case *ast.SelectorExpr: // os.Stderr.WriteString
				pkgName = "?"
			}
			if !textCall(pkgName, sel.Sel.Name) {
				return true
			}
			for _, a := range call.Args {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					if letters.MatchString(verbs.ReplaceAllString(s, "")) {
						t.Errorf("%s: user-facing text outside the catalogue: %s", fset.Position(lit.Pos()), lit.Value)
					}
				}
			}
			return true
		})
	}
	if used == 0 {
		t.Error("no i18n.T call found: the check is vacuous")
	}
}
