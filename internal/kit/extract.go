package kit

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Reasons for refusing an archive; they let the tests tell the rules apart
// and are never shown.
const (
	reasonNotArchive  = "NOT_ARCHIVE"
	reasonAbsolute    = "ABSOLUTE"
	reasonDotDot      = "DOT_DOT"
	reasonBadName     = "BAD_NAME"
	reasonLongName    = "LONG_NAME"
	reasonOutside     = "OUTSIDE"
	reasonReserved    = "RESERVED"
	reasonLink        = "LINK"
	reasonSpecial     = "SPECIAL"
	reasonDuplicate   = "DUPLICATE"
	reasonCase        = "CASE"
	reasonConflict    = "CONFLICT"
	reasonTooMany     = "TOO_MANY"
	reasonTooLarge    = "TOO_LARGE"
	reasonMissingRoot = "MISSING_ROOT"
)

const (
	cadreDir      = "_cadre"
	frameworkFile = ".sha256"
	maxComponent  = 255
	maxName       = 1024
	maxSumFile    = 128
)

var (
	// ErrUnsafe: the archive breaks a rule of extraction (path, type,
	// duplicate, size). Every *UnsafeError is ErrUnsafe for errors.Is.
	ErrUnsafe = errors.New(codeUnsafe)
	// ErrExists: ./<lab> already exists; nothing is replaced.
	ErrExists = errors.New(codeExists)
	// ErrFrameworkDiffers: ./_cadre exists and is not the framework of the
	// kit (other fingerprint, no fingerprint, or not a plain directory).
	ErrFrameworkDiffers = errors.New(codeFrameworkDiffers)
	// ErrWrite: the target directory cannot be written (rights, full disk).
	ErrWrite = errors.New(codeWrite)
)

// WriteError is a failure to write into the target directory; it is ErrWrite
// for errors.Is and unwraps to the error of the system.
type WriteError struct{ Err error }

func (e *WriteError) Error() string        { return codeWrite }
func (e *WriteError) Unwrap() error        { return e.Err }
func (e *WriteError) Is(target error) bool { return target == ErrWrite }

func writeFailed(err error) error {
	if err == nil {
		return nil
	}
	return &WriteError{Err: err}
}

// UnsafeError tells which rule an archive breaks. The name of the entry is
// not kept: it comes from the network.
type UnsafeError struct{ Reason string }

func (e *UnsafeError) Error() string        { return codeUnsafe + "_" + e.Reason }
func (e *UnsafeError) Is(target error) bool { return target == ErrUnsafe }

func unsafe(reason string) error { return &UnsafeError{Reason: reason} }

// Staged is a kit extracted into a staging directory of the target
// directory, not yet in place. Framework is the fingerprint of its _cadre/:
// SHA-256 of the sorted lines "name NUL mode NUL sha256(content)" of its
// files. Reused is set by Install when an identical _cadre/ was kept.
type Staged struct {
	Framework string
	Reused    bool
	staging   string
	lab       string
}

// Remove deletes the staging directory; it is safe to call more than once.
func (s *Staged) Remove() {
	if s != nil && s.staging != "" {
		_ = os.RemoveAll(s.staging)
	}
}

// Extract reads the tar.gz archive from its start and writes <lab>/ and _cadre/ into a new
// staging directory of dir. Every entry is checked before anything is
// written for it: regular files and directories only (no link of any kind,
// no device, no FIFO), relative names of printable ASCII under <lab>/ or
// _cadre/ without "." or "..", components of at most 255 bytes, no
// duplicate, no two names that differ only by case (the disk of a Mac does
// not tell them apart), at most MaxEntries entries and MaxTotal bytes.
// Files get 0755 when the archive marks them executable, 0644 otherwise.
// On error the staging directory is already removed.
func Extract(archive io.Reader, dir, lab string) (_ *Staged, err error) {
	if !ValidLab(lab) {
		return nil, ErrInvalidLab
	}
	staging, err := os.MkdirTemp(dir, ".sagnac-pull-")
	if err != nil {
		return nil, writeFailed(err)
	}
	s := &Staged{staging: staging, lab: lab}
	defer func() {
		if err != nil {
			s.Remove()
		}
	}()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return nil, unsafe(reasonNotArchive)
	}
	defer gz.Close()
	x := &extraction{
		root: staging, lab: lab,
		declared: map[string]bool{}, canon: map[string]string{}, isDir: map[string]bool{}, roots: map[string]bool{},
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unsafe(reasonNotArchive)
		}
		if err := x.add(hdr, tr); err != nil {
			return nil, err
		}
	}
	if !x.roots[lab] || !x.roots[cadreDir] {
		return nil, unsafe(reasonMissingRoot)
	}
	sort.Strings(x.framework)
	h := sha256.Sum256([]byte(strings.Join(x.framework, "")))
	s.Framework = hex.EncodeToString(h[:])
	return s, nil
}

type extraction struct {
	root, lab string
	count     int
	total     int64
	declared  map[string]bool   // names of the entries, as written
	canon     map[string]string // lower case name -> name, entries and their parents
	isDir     map[string]bool   // lower case name -> directory
	roots     map[string]bool
	framework []string
}

func (x *extraction) add(hdr *tar.Header, r io.Reader) error {
	if x.count++; x.count > MaxEntries {
		return unsafe(reasonTooMany)
	}
	name, dir := hdr.Name, false
	switch hdr.Typeflag {
	case tar.TypeReg:
	case tar.TypeDir:
		name, dir = strings.TrimSuffix(name, "/"), true
	case tar.TypeSymlink, tar.TypeLink:
		return unsafe(reasonLink)
	default:
		return unsafe(reasonSpecial)
	}
	if reason := checkName(name, x.lab); reason != "" {
		return unsafe(reason)
	}
	parts := strings.Split(name, "/")
	x.roots[parts[0]] = true
	if len(parts) == 1 && !dir {
		return unsafe(reasonConflict)
	}
	if err := x.register(name, parts, dir); err != nil {
		return err
	}
	target := filepath.Join(x.root, filepath.FromSlash(name))
	if dir {
		return writeFailed(os.MkdirAll(target, 0o755))
	}
	if hdr.Size < 0 || hdr.Size > MaxTotal-x.total {
		return unsafe(reasonTooLarge)
	}
	x.total += hdr.Size
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return writeFailed(err)
	}
	mode, perm := "644", os.FileMode(0o644)
	if hdr.Mode&0o111 != 0 {
		mode, perm = "755", 0o755
	}
	// The staging directory is new and holds no link; O_EXCL and O_NOFOLLOW
	// keep it so even if a rule above had a hole.
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return writeFailed(err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), r)
	if cerr := out.Close(); cerr != nil {
		return writeFailed(cerr)
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return writeFailed(err)
	}
	if err != nil || n != hdr.Size {
		return unsafe(reasonNotArchive)
	}
	if parts[0] == cadreDir {
		x.framework = append(x.framework, name+"\x00"+mode+"\x00"+hex.EncodeToString(h.Sum(nil))+"\n")
	}
	return nil
}

// register records name and its parents, refusing a second entry of the
// same name, a name that differs from a known one by case only, and a file
// where a directory is needed or the reverse.
func (x *extraction) register(name string, parts []string, dir bool) error {
	if x.declared[name] {
		return unsafe(reasonDuplicate)
	}
	for i := 1; i < len(parts); i++ {
		p := strings.Join(parts[:i], "/")
		lp := strings.ToLower(p)
		if c, ok := x.canon[lp]; ok {
			if c != p {
				return unsafe(reasonCase)
			}
			if !x.isDir[lp] {
				return unsafe(reasonConflict)
			}
			continue
		}
		x.canon[lp], x.isDir[lp] = p, true
	}
	lower := strings.ToLower(name)
	if c, ok := x.canon[lower]; ok {
		if c != name {
			return unsafe(reasonCase)
		}
		if x.isDir[lower] != dir {
			return unsafe(reasonConflict)
		}
	}
	x.canon[lower], x.isDir[lower], x.declared[name] = name, dir, true
	return nil
}

// checkName returns the reason for refusing name, or "".
func checkName(name, lab string) string {
	if name == "" {
		return reasonBadName
	}
	if name[0] == '/' {
		return reasonAbsolute
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == ".." {
			return reasonDotDot
		}
	}
	if len(name) > maxName {
		return reasonLongName
	}
	for i := 0; i < len(name); i++ {
		if b := name[i]; b < 0x20 || b > 0x7e || b == '\\' {
			return reasonBadName
		}
	}
	for _, p := range parts {
		if p == "" || p == "." {
			return reasonBadName
		}
		if len(p) > maxComponent {
			return reasonLongName
		}
	}
	if parts[0] != lab && parts[0] != cadreDir {
		return reasonOutside
	}
	if strings.EqualFold(name, cadreDir+"/"+frameworkFile) {
		return reasonReserved
	}
	return ""
}

// readFramework returns the fingerprint of dir/_cadre, computed from the
// files on disk: "" when there is no _cadre, ErrFrameworkDiffers when it is
// not a plain directory, was not written by sagnac pull (no .sha256 marker)
// or holds anything the kit did not put there. The fingerprint stored in
// .sha256 is not trusted: it only says that sagnac pull made the directory.
func readFramework(dir string) (string, error) {
	cadre := filepath.Join(dir, cadreDir)
	info, err := os.Lstat(cadre)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", writeFailed(err)
	}
	if !info.IsDir() {
		return "", ErrFrameworkDiffers
	}
	sumPath := filepath.Join(cadre, frameworkFile)
	info, err = os.Lstat(sumPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSumFile {
		return "", ErrFrameworkDiffers
	}
	raw, err := os.ReadFile(sumPath)
	if err != nil || !reSHA256.MatchString(strings.TrimSpace(string(raw))) {
		return "", ErrFrameworkDiffers
	}
	return diskFramework(cadre)
}

// diskFramework computes, from the files of cadre, the fingerprint that
// Extract computes from an archive (the marker, in any case, excepted). A
// link or a special file makes it ErrFrameworkDiffers.
func diskFramework(cadre string) (string, error) {
	var lines []string
	err := filepath.WalkDir(cadre, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == cadre || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(cadre, p)
		if err != nil {
			return err
		}
		if strings.EqualFold(rel, frameworkFile) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrFrameworkDiffers
		}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return ErrFrameworkDiffers
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		mode := "644"
		if info.Mode().Perm()&0o111 != 0 {
			mode = "755"
		}
		lines = append(lines, cadreDir+"/"+filepath.ToSlash(rel)+"\x00"+mode+"\x00"+hex.EncodeToString(h.Sum(nil))+"\n")
		return nil
	})
	if errors.Is(err, ErrFrameworkDiffers) {
		return "", err
	}
	if err != nil {
		return "", writeFailed(err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(sum[:]), nil
}

// CheckFramework tells, before any download, whether dir/_cadre can be
// reused at all: nil when it is absent or carries a fingerprint.
func CheckFramework(dir string) error {
	_, err := readFramework(dir)
	return err
}

// Install moves the staged kit into dir: <lab>/ always, _cadre/ unless dir
// already holds the same framework, which is then kept as is (Reused). An
// existing <lab> or another framework is refused before anything moves. If
// the lab cannot be moved, a _cadre/ moved by this call is taken back.
func (s *Staged) Install(dir string) error {
	target := filepath.Join(dir, s.lab)
	if _, err := os.Lstat(target); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return writeFailed(err)
	}
	existing, err := readFramework(dir)
	if err != nil {
		return err
	}
	if existing != "" && existing != s.Framework {
		return ErrFrameworkDiffers
	}
	staged := filepath.Join(s.staging, cadreDir)
	cadre := filepath.Join(dir, cadreDir)
	moved := false
	if existing == "" {
		if err := os.WriteFile(filepath.Join(staged, frameworkFile), []byte(s.Framework+"\n"), 0o644); err != nil {
			return writeFailed(err)
		}
		if err := os.Rename(staged, cadre); err != nil {
			return writeFailed(err)
		}
		moved = true
	} else {
		s.Reused = true
	}
	if err := os.Rename(filepath.Join(s.staging, s.lab), target); err != nil {
		if moved {
			_ = os.Rename(cadre, staged)
		}
		return writeFailed(err)
	}
	return nil
}
