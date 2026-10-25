// Package cleanup decides what "sagnac clean" may remove, and removes the
// half-written files. It only ever touches the Sagnac cache: the clusters of
// the labs (deleted by the lab framework's own script, not here) and the
// leftovers of an interrupted download or copy. Models, tool binaries and
// base images stay.
package cleanup

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrUnsafe: a path is empty, relative, outside the cache, a link, or not
// something this package removes. Nothing is removed.
var ErrUnsafe = errors.New(codeUnsafe)

// ErrNoCache: neither SAGNAC_CACHE nor a home directory tells where the cache is.
var ErrNoCache = errors.New(codeNoCache)

const (
	codeUnsafe  = "CLEAN_UNSAFE_PATH"
	codeNoCache = "CLEAN_NO_CACHE"
	clustersDir = "clusters"
)

var (
	// The name rule of bin/sagnac-cluster: lower case, digits and hyphens,
	// not starting with a hyphen.
	reCluster = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// What the framework leaves while it writes: "<name>.partiel.<pid>" in the
	// cache (downloads), ".workspace.partiel.<pid>" in a lab (copy of starter/).
	rePartial = regexp.MustCompile(`\.partiel\.[0-9]+$`)
	reLabCopy = regexp.MustCompile(`^\.workspace\.partiel\.[0-9]+$`)
)

// ValidCluster reports whether name is a cluster name of the lab framework.
func ValidCluster(name string) bool { return reCluster.MatchString(name) }

// Cluster is a cluster of the cache.
type Cluster struct {
	Name   string
	State  string // <cache>/clusters/<name>, removed with the cluster
	Exists bool   // the state directory is there
}

// Plan is what a clean would do.
type Plan struct {
	Clusters  []Cluster
	Temporary []string // half-written files of the cache
	Refused   []string // entries of clusters/ that were not planned (links)
}

// Root is the Sagnac cache: $SAGNAC_CACHE, else $HOME/.cache/sagnac. It is the
// cache of the lab framework (docs/contrat.md), which ignores XDG_CACHE_HOME.
// The path is absolute, with its links resolved. The filesystem root, the
// home directory and any directory that contains it are refused.
func Root(getenv func(string) string) (string, error) {
	dir := getenv("SAGNAC_CACHE")
	home := getenv("HOME")
	if dir == "" {
		if home == "" || !filepath.IsAbs(home) {
			return "", ErrNoCache
		}
		dir = filepath.Join(home, ".cache", "sagnac")
	}
	if !filepath.IsAbs(dir) {
		return "", ErrUnsafe
	}
	dir = resolve(dir)
	if dir == string(filepath.Separator) {
		return "", ErrUnsafe
	}
	if home != "" && filepath.IsAbs(home) {
		if inside(dir, resolve(home)) || dir == resolve(home) {
			// The home directory is the cache or lies inside it.
			return "", ErrUnsafe
		}
	}
	return dir, nil
}

// resolve cleans p and resolves the links of the part that exists.
func resolve(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return p
			}
			return filepath.Join(real, rest)
		}
		if filepath.Dir(cur) == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// inside reports whether child is strictly below dir (both clean).
func inside(dir, child string) bool {
	rel, err := filepath.Rel(dir, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func checkRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return "", ErrUnsafe
	}
	return resolve(root), nil
}

// Build plans a clean of the cache at root: the clusters named (or every
// cluster of the cache with all), and the half-written files. The root is
// the result of Root; nothing is removed here.
func Build(root string, names []string, all bool) (*Plan, error) {
	real, err := checkRoot(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	plan := &Plan{}
	dir := filepath.Join(root, clustersDir)
	// clusters/ itself must not lead out of the cache.
	if _, err := os.Lstat(dir); err == nil {
		if !inside(real, resolve(dir)) {
			return nil, ErrUnsafe
		}
	}
	seen := map[string]bool{}
	add := func(name string, named bool) error {
		if seen[name] {
			return nil
		}
		seen[name] = true
		state := filepath.Join(dir, name)
		info, err := os.Lstat(state)
		switch {
		case err != nil:
			plan.Clusters = append(plan.Clusters, Cluster{Name: name, State: state})
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			if named {
				return ErrUnsafe
			}
			plan.Refused = append(plan.Refused, state)
		default:
			plan.Clusters = append(plan.Clusters, Cluster{Name: name, State: state, Exists: true})
		}
		return nil
	}
	for _, name := range names {
		if !ValidCluster(name) {
			return nil, ErrUnsafe
		}
		if err := add(name, true); err != nil {
			return nil, err
		}
	}
	if all {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if !ValidCluster(e.Name()) {
				continue
			}
			if err := add(e.Name(), false); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(plan.Clusters, func(i, j int) bool { return plan.Clusters[i].Name < plan.Clusters[j].Name })
	sort.Strings(plan.Refused)
	if plan.Temporary, err = scanPartial(root); err != nil {
		return nil, err
	}
	return plan, nil
}

// scanPartial lists the half-written entries two levels below root, never
// following a link and never looking inside clusters/ (the framework removes
// it with the cluster). Deeper names belong to what a tool extracted.
func scanPartial(root string) ([]string, error) {
	var found []string
	top, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range top {
		p := filepath.Join(root, e.Name())
		if rePartial.MatchString(e.Name()) {
			found = append(found, p)
			continue
		}
		if e.Name() == clustersDir || !e.IsDir() {
			continue // IsDir is false for a link: not followed
		}
		sub, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, s := range sub {
			if rePartial.MatchString(s.Name()) {
				found = append(found, filepath.Join(p, s.Name()))
			}
		}
	}
	sort.Strings(found)
	return found, nil
}

// LabTemporary lists the partial copy of the starter files that an interrupted
// "make setup" left in a lab directory. The workspace is the student's work
// and is never listed.
func LabTemporary(labDir string) ([]string, error) {
	if _, err := checkRoot(labDir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(labDir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && reLabCopy.MatchString(e.Name()) {
			found = append(found, filepath.Join(labDir, e.Name()))
		}
	}
	return found, nil
}

// RemoveTemporary removes half-written entries, each checked on its own: an
// absolute path whose parent, links resolved, is below root, with the name of
// a leftover. A link is removed as a link, its target is left alone. The first
// refusal stops the removal.
func RemoveTemporary(root string, paths []string) error {
	real, err := checkRoot(root)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if p == "" || !filepath.IsAbs(p) {
			return ErrUnsafe
		}
		p = filepath.Clean(p)
		name := filepath.Base(p)
		if !rePartial.MatchString(name) && !reLabCopy.MatchString(name) {
			return ErrUnsafe
		}
		parent := resolve(filepath.Dir(p))
		if parent != real && !inside(real, parent) {
			return ErrUnsafe
		}
		if err := os.RemoveAll(filepath.Join(parent, name)); err != nil {
			return err
		}
	}
	return nil
}
