package kit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry is one member of a test archive; typ 0 is a regular file.
type entry struct {
	name string
	typ  byte
	body string
	mode int64
	link string
}

func file(name, body string) entry {
	return entry{name: name, typ: tar.TypeReg, body: body, mode: 0o644}
}
func executable(name, body string) entry {
	return entry{name: name, typ: tar.TypeReg, body: body, mode: 0o755}
}
func dir(name string) entry { return entry{name: name, typ: tar.TypeDir, mode: 0o755} }

// goodKit has the layout of a kit built by the platform (docs/contrat.md,
// section 1): the lab, then the framework,
// with _cadre/bin/sagnac-tools but no _cadre/bin entry.
func goodKit() []entry {
	return []entry{
		dir("hello"),
		file("hello/Makefile", "include ../_cadre/lab.mk\n"),
		file("hello/meta.yaml", "id: hello\nversion: 3\n"),
		dir("hello/checks"),
		executable("hello/checks/verify_bonjour.sh", "#!/bin/sh\nexit 0\n"),
		dir("hello/.sagnac"),
		file("hello/.sagnac/tools.linux-amd64.manifest", "jq 1.7\n"),
		dir("_cadre"),
		file("_cadre/lab.mk", "setup:\n"),
		dir("_cadre/lib"),
		file("_cadre/lib/controle.sh", "sagnac_tirer() { :; }\n"),
		executable("_cadre/bin/sagnac-tools", "#!/bin/sh\n"),
	}
}

func build(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Format: tar.FormatPAX}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// workdir returns an empty directory inside a parent, so that a write outside
// the directory can be seen in the parent.
func workdir(t *testing.T) (parent, work string) {
	parent = t.TempDir()
	work = filepath.Join(parent, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	return parent, work
}

func writeArchive(t *testing.T, raw []byte) *bytes.Reader {
	t.Helper()
	return bytes.NewReader(raw)
}

func listing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if p != root {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// pullInto extracts and installs the archive into work.
func pullInto(t *testing.T, work string, raw []byte) (*Staged, error) {
	t.Helper()
	staged, err := Extract(writeArchive(t, raw), work, "hello")
	if err != nil {
		return nil, err
	}
	defer staged.Remove()
	return staged, staged.Install(work)
}

func TestGoodKitInstalled(t *testing.T) {
	_, work := workdir(t)
	staged, err := pullInto(t, work, build(t, goodKit()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(work, "hello", "Makefile"))
	if err != nil || string(got) != "include ../_cadre/lab.mk\n" {
		t.Fatalf("Makefile = %q, %v", got, err)
	}
	for name, want := range map[string]os.FileMode{
		"hello/checks/verify_bonjour.sh": 0o755, "_cadre/bin/sagnac-tools": 0o755,
		"hello/meta.yaml": 0o644, "_cadre/lab.mk": 0o644,
	} {
		info, err := os.Stat(filepath.Join(work, name))
		if err != nil || info.Mode().Perm()&0o111 != want&0o111 {
			t.Errorf("%s: mode %v, %v; want executable bit of %v", name, info.Mode(), err, want)
		}
	}
	sum, err := os.ReadFile(filepath.Join(work, "_cadre", ".sha256"))
	if err != nil || strings.TrimSpace(string(sum)) != staged.Framework || len(staged.Framework) != 64 {
		t.Errorf("_cadre/.sha256 = %q, framework %q, %v", sum, staged.Framework, err)
	}
	for _, name := range listing(t, work) {
		if strings.HasPrefix(name, ".sagnac-") {
			t.Errorf("staging left behind: %s", name)
		}
	}
}

func TestModesKeepOnlyTheExecutableBit(t *testing.T) {
	_, work := workdir(t)
	entries := append(goodKit(), entry{name: "hello/setuid", typ: tar.TypeReg, body: "x", mode: 0o4777},
		entry{name: "hello/world", typ: tar.TypeReg, body: "x", mode: 0o666})
	if _, err := pullInto(t, work, build(t, entries)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"hello/setuid": 0o755, "hello/world": 0o644} {
		info, err := os.Stat(filepath.Join(work, name))
		if err != nil || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm()&^0o022 != want&^0o022 {
			t.Errorf("%s: mode %v, want %v", name, info.Mode(), want)
		}
	}
}

// An archive that escapes is refused and nothing is written,
// neither in the target directory nor next to it.
func TestUnsafeArchivesRefused(t *testing.T) {
	cases := map[string]struct {
		extra  []entry
		reason string
	}{
		"dot dot":            {[]entry{file("../evil", "x")}, reasonDotDot},
		"dot dot inside":     {[]entry{file("hello/../../evil", "x")}, reasonDotDot},
		"absolute":           {[]entry{file("/tmp/evil", "x")}, reasonAbsolute},
		"symlink out":        {[]entry{{name: "hello/link", typ: tar.TypeSymlink, link: "../../"}, file("hello/link/evil", "x")}, reasonLink},
		"symlink in":         {[]entry{{name: "hello/link", typ: tar.TypeSymlink, link: "Makefile"}}, reasonLink},
		"hard link":          {[]entry{{name: "hello/hard", typ: tar.TypeLink, link: "hello/Makefile"}}, reasonLink},
		"char device":        {[]entry{{name: "hello/tty", typ: tar.TypeChar}}, reasonSpecial},
		"block device":       {[]entry{{name: "hello/disk", typ: tar.TypeBlock}}, reasonSpecial},
		"fifo":               {[]entry{{name: "hello/fifo", typ: tar.TypeFifo}}, reasonSpecial},
		"outside the roots":  {[]entry{file("other/x", "x")}, reasonOutside},
		"other lab":          {[]entry{file("hello2/x", "x")}, reasonOutside},
		"duplicate":          {[]entry{file("hello/Makefile", "again")}, reasonDuplicate},
		"case collision":     {[]entry{file("hello/makefile", "x")}, reasonCase},
		"long component":     {[]entry{file("hello/"+strings.Repeat("a", 256), "x")}, reasonLongName},
		"dot component":      {[]entry{file("hello/./x", "x")}, reasonBadName},
		"empty component":    {[]entry{file("hello//x", "x")}, reasonBadName},
		"backslash":          {[]entry{file(`hello/..\..\x`, "x")}, reasonBadName},
		"control character":  {[]entry{file("hello/a\nb", "x")}, reasonBadName},
		"reserved name":      {[]entry{file("_cadre/.sha256", "0")}, reasonReserved},
		"reserved name case": {[]entry{file("_cadre/.SHA256", "0")}, reasonReserved},
		"reserved mixed":     {[]entry{file("_cadre/.Sha256", "0")}, reasonReserved},
		"file under a file":  {[]entry{file("hello/Makefile/x", "x")}, reasonConflict},
		"directory on file":  {[]entry{dir("hello/meta.yaml")}, reasonDuplicate},
		"bare root file":     {[]entry{file("_cadre2", "x")}, reasonOutside},
		"global pax headers": {[]entry{{name: "pax", typ: tar.TypeXGlobalHeader}}, reasonSpecial},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			parent, work := workdir(t)
			_, err := pullInto(t, work, build(t, append(goodKit(), c.extra...)))
			var unsafe *UnsafeError
			if !errors.As(err, &unsafe) || unsafe.Reason != c.reason || !errors.Is(err, ErrUnsafe) {
				t.Fatalf("err = %v, want reason %s", err, c.reason)
			}
			if got := listing(t, parent); len(got) != 1 || got[0] != "work" {
				t.Errorf("written: %v", got)
			}
		})
	}
}

func TestMissingRootRefused(t *testing.T) {
	for _, root := range []string{"hello", "_cadre"} {
		var entries []entry
		for _, e := range goodKit() {
			if e.name != root && !strings.HasPrefix(e.name, root+"/") {
				entries = append(entries, e)
			}
		}
		_, work := workdir(t)
		_, err := pullInto(t, work, build(t, entries))
		var unsafe *UnsafeError
		if !errors.As(err, &unsafe) || unsafe.Reason != reasonMissingRoot {
			t.Errorf("without %s: err = %v", root, err)
		}
		if got := listing(t, work); len(got) != 0 {
			t.Errorf("without %s: written %v", root, got)
		}
	}
}

func TestBombsRefused(t *testing.T) {
	oldEntries, oldTotal := MaxEntries, MaxTotal
	t.Cleanup(func() { MaxEntries, MaxTotal = oldEntries, oldTotal })
	MaxEntries, MaxTotal = len(goodKit())+2, 1<<20

	_, work := workdir(t)
	many := append(goodKit(), file("hello/a", "1"), file("hello/b", "2"), file("hello/c", "3"))
	_, err := pullInto(t, work, build(t, many))
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) || unsafe.Reason != reasonTooMany {
		t.Errorf("too many entries: err = %v", err)
	}
	big := append(goodKit(), file("hello/zeros", strings.Repeat("\x00", 1<<20)))
	_, err = pullInto(t, work, build(t, big))
	if !errors.As(err, &unsafe) || unsafe.Reason != reasonTooLarge {
		t.Errorf("too large: err = %v", err)
	}
	if got := listing(t, work); len(got) != 0 {
		t.Errorf("written %v", got)
	}
}

func TestNotAnArchive(t *testing.T) {
	_, work := workdir(t)
	_, err := Extract(writeArchive(t, []byte("<html>proxy</html>")), work, "hello")
	if !errors.Is(err, ErrUnsafe) {
		t.Errorf("err = %v", err)
	}
	if got := listing(t, work); len(got) != 0 {
		t.Errorf("written %v", got)
	}
}

func TestExistingLabRefused(t *testing.T) {
	_, work := workdir(t)
	if err := os.Mkdir(filepath.Join(work, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := pullInto(t, work, build(t, goodKit()))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if got := listing(t, work); len(got) != 1 {
		t.Errorf("written %v", got)
	}
}

func TestFrameworkReusedOrRefused(t *testing.T) {
	_, work := workdir(t)
	if _, err := pullInto(t, work, build(t, goodKit())); err != nil {
		t.Fatal(err)
	}
	// A second lab with the same framework: _cadre is kept as is.
	other := []entry{dir("autre"), file("autre/Makefile", "x")}
	for _, e := range goodKit() {
		if strings.HasPrefix(e.name, "_cadre") {
			other = append(other, e)
		}
	}
	staged, err := Extract(writeArchive(t, build(t, other)), work, "autre")
	if err != nil {
		t.Fatal(err)
	}
	if err := staged.Install(work); err != nil {
		t.Fatalf("identical framework: %v", err)
	}
	staged.Remove()
	if !staged.Reused {
		t.Error("identical framework not reported as reused")
	}
	// A third lab whose framework differs: refused, nothing written.
	third := []entry{dir("trois"), file("trois/Makefile", "x"), dir("_cadre"), file("_cadre/lab.mk", "changed\n")}
	staged, err = Extract(writeArchive(t, build(t, third)), work, "trois")
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Remove()
	if err := staged.Install(work); !errors.Is(err, ErrFrameworkDiffers) {
		t.Fatalf("different framework: err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(work, "trois")); !os.IsNotExist(err) {
		t.Errorf("trois written: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "_cadre", "lab.mk")); string(got) != "setup:\n" {
		t.Errorf("_cadre changed: %q", got)
	}
}

func TestCheckFramework(t *testing.T) {
	_, work := workdir(t)
	if err := CheckFramework(work); err != nil {
		t.Errorf("no _cadre: %v", err)
	}
	cadre := filepath.Join(work, "_cadre")
	if err := os.Mkdir(cadre, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckFramework(work); !errors.Is(err, ErrFrameworkDiffers) {
		t.Errorf("_cadre without .sha256: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cadre, ".sha256"), []byte(strings.Repeat("a", 64)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckFramework(work); err != nil {
		t.Errorf("_cadre with .sha256: %v", err)
	}
	// A _cadre that is a link is never reused.
	_, link := workdir(t)
	if err := os.Symlink(cadre, filepath.Join(link, "_cadre")); err != nil {
		t.Fatal(err)
	}
	if err := CheckFramework(link); !errors.Is(err, ErrFrameworkDiffers) {
		t.Errorf("_cadre link: %v", err)
	}
}

func TestValidLab(t *testing.T) {
	for _, ok := range []string{"hello", "b1-m1-lab2", "a"} {
		if !ValidLab(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "..", "../x", "Hello", "-x", "a/b", "_cadre", strings.Repeat("a", 49), "a b", "hello%2f"} {
		if ValidLab(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The stored fingerprint is not trusted: what _cadre/ holds on disk is hashed
// again, and a changed, added or linked file makes it another framework.
func TestFrameworkRecomputedFromDisk(t *testing.T) {
	tamper := map[string]func(t *testing.T, cadre string){
		"changed": func(t *testing.T, cadre string) {
			if err := os.WriteFile(filepath.Join(cadre, "lab.mk"), []byte("evil:\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"added": func(t *testing.T, cadre string) {
			if err := os.WriteFile(filepath.Join(cadre, "extra.sh"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"mode": func(t *testing.T, cadre string) {
			if err := os.Chmod(filepath.Join(cadre, "lab.mk"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"link": func(t *testing.T, cadre string) {
			p := filepath.Join(cadre, "lab.mk")
			_ = os.Remove(p)
			if err := os.Symlink("/etc/hosts", p); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range tamper {
		_, work := workdir(t)
		if _, err := pullInto(t, work, build(t, goodKit())); err != nil {
			t.Fatal(err)
		}
		change(t, filepath.Join(work, "_cadre"))
		other := []entry{dir("autre"), file("autre/Makefile", "x")}
		for _, e := range goodKit() {
			if strings.HasPrefix(e.name, "_cadre") {
				other = append(other, e)
			}
		}
		staged, err := Extract(writeArchive(t, build(t, other)), work, "autre")
		if err != nil {
			t.Fatal(err)
		}
		if err := staged.Install(work); !errors.Is(err, ErrFrameworkDiffers) {
			t.Errorf("%s: err = %v", name, err)
		}
		staged.Remove()
		if _, err := os.Lstat(filepath.Join(work, "autre")); !os.IsNotExist(err) {
			t.Errorf("%s: autre written", name)
		}
	}
}

// A framework the person left as it was, .sha256 aside, is still reused.
func TestFrameworkUntouchedStillReused(t *testing.T) {
	_, work := workdir(t)
	if _, err := pullInto(t, work, build(t, goodKit())); err != nil {
		t.Fatal(err)
	}
	if err := CheckFramework(work); err != nil {
		t.Errorf("CheckFramework: %v", err)
	}
}
