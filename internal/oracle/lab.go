// Package oracle runs the checks of a lab under contract v2 of the oracle
// (docs/contrat.md) and builds the result of schema sagnac-result/2. Its
// behaviour follows the reference executor of the platform; the conformance
// tests compare both on the frozen kits of testdata/.
package oracle

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const (
	defaultDelaySeconds = 10
	defaultDurationMax  = 3_600_000
	accessExecute       = 1 // X_OK for syscall.Access
)

// capSeconds bounds timeout_s of one check: 60 s without GenAI, 240 s with.
// It is also the ceiling of one run, except for a lab with a cluster and
// GenAI (runCeilingSeconds).
func capSeconds(genAI bool) int64 {
	if genAI {
		return 240
	}
	return 60
}

// clusterGenAICeilingSeconds is the ceiling of one run of a lab with a
// cluster and GenAI: its checks start workloads on the cluster before they
// can ask anything, and 240 s left no margin on a loaded machine.
const clusterGenAICeilingSeconds = 360

// runCeilingSeconds is the ceiling of one run of the lab (docs/contrat.md,
// section 3): 60 s, 240 s with GenAI, 360 s with GenAI and a cluster.
func runCeilingSeconds(lab *Lab) int64 {
	if lab.GenAI && lab.Cluster != "" {
		return clusterGenAICeilingSeconds
	}
	return capSeconds(lab.GenAI)
}

var (
	reLab   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	reCheck = regexp.MustCompile(`^verify_[a-z0-9_]{1,48}$`)
	reCode  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)
	reEnum  = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
)

// Value types of the observed and expected whitelists.
const (
	typeBool     = "boolean"
	typeInt      = "integer"
	typeEnum     = "enum"
	typeDuration = "duration_ms"
)

// UsageError is a wrong use of the command (exit code 64): the directory is
// not a lab, the workspace does not exist.
type UsageError struct {
	Reason string
	Path   string
}

func (e *UsageError) Error() string { return e.Path + ": " + e.Reason }

// ExitCode is 64.
func (e *UsageError) ExitCode() int { return ExitUsage }

// KitError is a kit that breaks the contract (meta.yaml, checks/): an error
// of the oracle, exit code 3 (ORACLE_INVALID_KIT).
type KitError struct {
	Where  string
	Reason string
}

func (e *KitError) Error() string { return "ORACLE_INVALID_KIT: " + e.Where + ": " + e.Reason }

// ExitCode is 3.
func (e *KitError) ExitCode() int { return ExitOracle }

func kitErr(where, reason string) error { return &KitError{Where: where, Reason: reason} }

// ValueSpec is one entry of the typed whitelist of what a check may send.
type ValueSpec struct {
	Type   string
	Min    int64
	Max    int64
	Values []string
}

// Check is one control declared in meta.yaml.
type Check struct {
	ID            string
	Step          int64
	Script        string // absolute path
	DelaySeconds  int64
	DependsOn     []string
	Codes         []string
	Preconditions []string
	Observed      ValueSpec
	Expected      ValueSpec
}

// Lab is a loaded and validated lab kit.
type Lab struct {
	Dir         string // absolute, symbolic links resolved
	ID          string
	Version     int64
	GenAI       bool
	Certifiable bool
	// NetworkDuringCheck: network_during_check is true, the checks keep the
	// network; false or absent keeps it cut.
	NetworkDuringCheck bool
	Artifacts          []string
	Preconditions      map[string]string
	Checks             []Check
	// Docker: docker is true, the platform of the result names the
	// container engine.
	Docker bool
	// Cluster is the name of the local Kubernetes cluster of the lab, empty
	// without one (docs/contrat.md, section 7); APIPort is the port of its
	// API on 127.0.0.1, read in _cadre/clusters/<name>.conf.
	Cluster string
	APIPort int
	// Teardown is checks/teardown.sh when the lab has one, run after the
	// last check.
	Teardown string
	libPath  string // _cadre/lib/controle.sh of the kit
}

// resolve makes dir absolute and resolves symbolic links as far as the path
// exists, like Python's Path.resolve(strict=False).
func resolve(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func isRegular(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func executable(p string) bool { return syscall.Access(p, accessExecute) == nil }

// truthy is Python's truth value for the YAML values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// boolean reads a boolean of meta.yaml: true or false, nothing else (the
// oui and non of contract v1 are refused).
func boolean(v any, field string) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, kitErr("meta.yaml", field+" is true or false")
}

func asInt(v any) (int64, bool) {
	n, ok := v.(int64)
	return n, ok
}

// listOrEmpty is `value or []`: a falsy value is an empty list, a list is
// itself, anything else is not a list.
func listOrEmpty(v any) ([]any, bool) {
	if !truthy(v) {
		return nil, true
	}
	l, ok := v.([]any)
	return l, ok
}

// Closed lists of the keys of contract v2 (docs/contrat.md, section 2): any
// other key, of contract v1 included, is refused.
var (
	metaKeys = keySet("id", "version", "title", "type", "difficulty", "target_minutes", "objectives",
		"prerequisites", "tools", "genai", "gpu", "docker", "network_during_check", "max_memory_mb",
		"certifiable", "artifacts", "preconditions", "checks", "cluster")
	preconditionKeys = keySet("code", "description")
	checkKeys        = keySet("id", "step", "script", "timeout_s", "depends_on", "codes", "preconditions",
		"observed", "expected")
	specKeys     = keySet("type", "min", "max", "values")
	metaBooleans = []string{"genai", "gpu", "docker", "network_during_check", "certifiable"}
)

func keySet(keys ...string) map[string]bool {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// closedKeys refuses any key of m outside allowed, naming them.
func closedKeys(m map[string]any, allowed map[string]bool, where string) error {
	var unknown []string
	for k := range m {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return kitErr(where, "unknown key of contract v2: "+strings.Join(unknown, ", "))
}

func validateSpec(raw any, where string) (ValueSpec, error) {
	m, ok := raw.(map[string]any)
	if ok {
		if err := closedKeys(m, specKeys, where); err != nil {
			return ValueSpec{}, err
		}
	}
	t, _ := m["type"].(string)
	if !ok || (t != typeBool && t != typeInt && t != typeEnum && t != typeDuration) {
		return ValueSpec{}, kitErr(where, "type is boolean, integer, enum or duration_ms")
	}
	spec := ValueSpec{Type: t}
	switch t {
	case typeInt:
		lo, okLo := asInt(m["min"])
		hi, okHi := asInt(m["max"])
		if !okLo || !okHi || lo > hi {
			return ValueSpec{}, kitErr(where, "an integer has integer min and max, min <= max")
		}
		spec.Min, spec.Max = lo, hi
	case typeDuration:
		hi := int64(defaultDurationMax)
		if v, present := m["max"]; present {
			n, ok := asInt(v)
			if !ok || n < 0 {
				return ValueSpec{}, kitErr(where, "invalid max")
			}
			hi = n
		}
		spec.Min, spec.Max = 0, hi
	case typeEnum:
		values, ok := m["values"].([]any)
		if !ok || len(values) == 0 {
			return ValueSpec{}, kitErr(where, "values is a closed list of [a-z0-9_] identifiers")
		}
		for _, v := range values {
			s, ok := v.(string)
			if !ok || !reEnum.MatchString(s) || slices.Contains(spec.Values, s) {
				return ValueSpec{}, kitErr(where, "values is a closed list of [a-z0-9_] identifiers")
			}
			spec.Values = append(spec.Values, s)
		}
	}
	return spec, nil
}

// findLib locates lib/controle.sh of the cadre next to the lab: <lab>/../_cadre
// (kit and simple labs) or <lab>/../../_cadre (labs grouped by module), like
// the Makefile of a lab.
func findLib(dir string) (string, error) {
	for _, up := range []string{"..", filepath.Join("..", "..")} {
		p := filepath.Join(dir, up, "_cadre", "lib", "controle.sh")
		if isRegular(p) {
			return p, nil
		}
	}
	return "", kitErr("_cadre/lib/controle.sh", "not found next to the lab")
}

// LoadLab reads and validates meta.yaml and checks/ of the lab in dir, with
// the rules of docs/contrat.md, section 2. A directory without meta.yaml
// gives a *UsageError, a kit that breaks the contract a *KitError.
func LoadLab(dir string) (*Lab, error) {
	dir = resolve(dir)
	metaPath := filepath.Join(dir, "meta.yaml")
	if !isRegular(metaPath) {
		return nil, &UsageError{Reason: "no meta.yaml, not a lab", Path: dir}
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, kitErr("meta.yaml", "unreadable")
	}
	doc, err := parseYAML(string(raw))
	if err != nil {
		return nil, kitErr("meta.yaml", err.Error())
	}
	meta, ok := doc.(map[string]any)
	if !ok {
		return nil, kitErr("meta.yaml", "mapping expected")
	}
	lab := &Lab{Dir: dir, Preconditions: map[string]string{}}
	if lab.ID, ok = meta["id"].(string); !ok || !reLab.MatchString(lab.ID) {
		return nil, kitErr("meta.yaml", "invalid id")
	}
	if lab.Version, ok = asInt(meta["version"]); !ok || lab.Version < 1 {
		return nil, kitErr("meta.yaml", "version is an integer >= 1")
	}
	if err := closedKeys(meta, metaKeys, "meta.yaml"); err != nil {
		return nil, err
	}
	for _, field := range metaBooleans {
		if _, err := boolean(valueOr(meta, field, false), field); err != nil {
			return nil, err
		}
	}
	lab.GenAI, _ = boolean(valueOr(meta, "genai", false), "genai")
	lab.Certifiable, _ = boolean(valueOr(meta, "certifiable", false), "certifiable")
	lab.NetworkDuringCheck, _ = boolean(valueOr(meta, "network_during_check", false), "network_during_check")
	lab.Docker, _ = boolean(valueOr(meta, "docker", false), "docker")
	if err := lab.loadCluster(meta); err != nil {
		return nil, err
	}
	if lab.Artifacts, err = artifacts(meta["artifacts"], lab.Certifiable); err != nil {
		return nil, err
	}

	pres, ok := listOrEmpty(meta["preconditions"])
	if !ok {
		return nil, kitErr("meta.yaml", "precondition 1 without a valid code")
	}
	for i, p := range pres {
		m, _ := p.(map[string]any)
		if m != nil {
			if err := closedKeys(m, preconditionKeys, "meta.yaml, precondition "+strconv.Itoa(i+1)); err != nil {
				return nil, err
			}
		}
		code, okCode := m["code"].(string)
		if m == nil || !okCode || !reCode.MatchString(code) {
			return nil, kitErr("meta.yaml", "precondition "+strconv.Itoa(i+1)+" without a valid code")
		}
		desc, okDesc := m["description"].(string)
		if !okDesc {
			return nil, kitErr("meta.yaml", "precondition "+code+" without description")
		}
		lab.Preconditions[code] = desc
	}
	if lab.Cluster != "" {
		for _, code := range []string{codeClusterDirty, codeClusterBusy} {
			if _, declared := lab.Preconditions[code]; !declared {
				return nil, kitErr("meta.yaml", "a lab with a cluster declares the precondition "+code)
			}
		}
	}

	checks, ok := meta["checks"].([]any)
	if !ok || len(checks) == 0 {
		return nil, kitErr("meta.yaml", "checks is a non-empty list")
	}
	seen := map[string]bool{}
	labCodes := map[string]bool{}
	for code := range lab.Preconditions {
		labCodes[code] = true
	}
	limit := capSeconds(lab.GenAI)
	for i, raw := range checks {
		where := "meta.yaml, check " + strconv.Itoa(i+1)
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, kitErr(where, "mapping expected")
		}
		if err := closedKeys(m, checkKeys, where); err != nil {
			return nil, err
		}
		var c Check
		if c.ID, ok = m["id"].(string); !ok || !reCheck.MatchString(c.ID) || seen[c.ID] {
			return nil, kitErr(where, "id missing, invalid (verify_...) or repeated")
		}
		where = "meta.yaml, " + c.ID
		if c.Step, ok = asInt(m["step"]); !ok || c.Step < 1 {
			return nil, kitErr(where, "step is an integer >= 1")
		}
		script, ok := m["script"].(string)
		if !ok || !strings.HasPrefix(script, "checks/") || slices.Contains(strings.Split(script, "/"), "..") {
			return nil, kitErr(where, "script is a path under checks/")
		}
		c.Script = filepath.Join(dir, script)
		if !isRegular(c.Script) || !executable(c.Script) {
			return nil, kitErr(where, script+" missing or not executable")
		}
		if c.DelaySeconds, ok = asInt(valueOr(m, "timeout_s", int64(defaultDelaySeconds))); !ok ||
			c.DelaySeconds < 1 || c.DelaySeconds > limit {
			return nil, kitErr(where, "timeout_s between 1 and "+strconv.FormatInt(limit, 10))
		}
		deps, ok := listOrEmpty(m["depends_on"])
		if !ok {
			return nil, kitErr(where, "depends_on only names checks declared before")
		}
		for _, d := range deps {
			s, isStr := d.(string)
			if !isStr || !seen[s] {
				return nil, kitErr(where, "depends_on only names checks declared before")
			}
			c.DependsOn = append(c.DependsOn, s)
		}
		codes, ok := m["codes"].([]any)
		if !ok || len(codes) == 0 {
			return nil, kitErr(where, "codes lists the stable failure codes (UPPER_CASE)")
		}
		for _, x := range codes {
			s, isStr := x.(string)
			if !isStr || !reCode.MatchString(s) {
				return nil, kitErr(where, "codes lists the stable failure codes (UPPER_CASE)")
			}
			c.Codes = append(c.Codes, s)
		}
		for _, s := range c.Codes {
			if labCodes[s] {
				return nil, kitErr(where, "code already used by another check or a precondition")
			}
		}
		for _, s := range c.Codes {
			labCodes[s] = true
		}
		pre, ok := listOrEmpty(m["preconditions"])
		if !ok {
			return nil, kitErr(where, "preconditions names codes declared at the top of meta.yaml")
		}
		for _, p := range pre {
			s, isStr := p.(string)
			if _, declared := lab.Preconditions[s]; !isStr || !declared {
				return nil, kitErr(where, "preconditions names codes declared at the top of meta.yaml")
			}
			c.Preconditions = append(c.Preconditions, s)
		}
		if c.Observed, err = validateSpec(valueOr(m, "observed", map[string]any{"type": typeBool}), where+", observed"); err != nil {
			return nil, err
		}
		c.Expected = c.Observed
		if v, present := m["expected"]; present {
			if c.Expected, err = validateSpec(v, where+", expected"); err != nil {
				return nil, err
			}
		}
		seen[c.ID] = true
		lab.Checks = append(lab.Checks, c)
	}
	for i := 1; i < len(lab.Checks); i++ {
		if lab.Checks[i].Step < lab.Checks[i-1].Step {
			return nil, kitErr("meta.yaml", "checks are listed in the order of the steps")
		}
	}
	for code := range labCodes {
		if !isRegular(filepath.Join(dir, "checks", "messages", "fr", code+".txt")) {
			return nil, kitErr("checks/messages/fr/"+code+".txt", "missing (French is the reference language)")
		}
	}
	teardown := filepath.Join(dir, "checks", "teardown.sh")
	if _, err := os.Stat(teardown); err == nil {
		if !isRegular(teardown) || !executable(teardown) {
			return nil, kitErr("checks/teardown.sh", "not executable")
		}
		lab.Teardown = teardown
	}
	if lab.libPath, err = findLib(dir); err != nil {
		return nil, err
	}
	return lab, nil
}

func valueOr(m map[string]any, key string, def any) any {
	if v, present := m[key]; present {
		return v
	}
	return def
}

// artifacts reads `artifacts`: a closed list of relative file paths for a
// certifiable lab, anything Python's list() accepts otherwise.
func artifacts(v any, certifiable bool) ([]string, error) {
	if certifiable {
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			return nil, kitErr("meta.yaml", "a certifiable lab declares the closed list artifacts")
		}
		var out []string
		for _, a := range list {
			s, ok := a.(string)
			if !ok || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") ||
				slices.Contains(strings.Split(s, "/"), "..") {
				return nil, kitErr("meta.yaml", "invalid artefact (relative file path)")
			}
			out = append(out, s)
		}
		return out, nil
	}
	if !truthy(v) {
		return nil, nil
	}
	switch x := v.(type) {
	case []any:
		var out []string
		for _, a := range x {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out, nil
	case string:
		return strings.Split(x, ""), nil
	case map[string]any:
		var out []string
		for k := range x {
			out = append(out, k)
		}
		slices.Sort(out)
		return out, nil
	}
	return nil, kitErr("meta.yaml", "artifacts is a list")
}
