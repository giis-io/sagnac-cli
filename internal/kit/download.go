// Package kit downloads the kit of a lab (GET /v1/kits/…), checks its SHA-256
// and its signature by a key pinned in the binary (signature.go), and extracts it into the working directory without ever writing outside
// <lab>/ and _cadre/ (docs/contrat.md, section 1).
// The token goes in the Authorization header only, through the client of the
// account package, which follows no redirect.
package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
)

// Codes of the errors of this package; the command maps them to a catalogue
// message and never shows them.
const (
	codeChecksum         = "KIT_CHECKSUM_MISMATCH"
	codeInvalidLab       = "KIT_INVALID_LAB"
	codeExists           = "KIT_LAB_EXISTS"
	codeFrameworkDiffers = "KIT_FRAMEWORK_DIFFERS"
	codeUnsafe           = "KIT_UNSAFE"
	codeWrite            = "KIT_WRITE_FAILED"
)

var (
	// ErrChecksum: the archive does not have the digest announced by the
	// platform (header of the download or kit_sha256 of the current version).
	ErrChecksum = errors.New(codeChecksum)
	// ErrInvalidLab: the lab identifier is not of the platform's pattern; it
	// is never put in an address.
	ErrInvalidLab = errors.New(codeInvalidLab)
)

// Limits of a kit; variables so that the tests can lower them. A real kit
// is a few hundred kilobytes.
var (
	MaxArchive int64 = 64 << 20 // compressed, as downloaded
	MaxTotal   int64 = 64 << 20 // sum of the sizes of the files, extracted
	MaxEntries       = 10000
)

const (
	maxBody          = 64 << 10
	downloadTimeout  = 10 * time.Minute
	headerSHA256     = "X-Sagnac-Kit-Sha256"
	maxVersionNumber = 1 << 31
)

var (
	reLab    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidLab reports whether lab is a lab identifier, with the same grammar as
// the platform.
func ValidLab(lab string) bool { return reLab.MatchString(lab) }

// Info is the current version of a lab, as GET /v1/kits/<lab> gives it.
type Info struct {
	Lab     string
	Version int
	SHA256  string
}

// Current returns the current version of lab and the digest of its kit.
func Current(ctx context.Context, c *account.Client, lab string) (*Info, error) {
	if !ValidLab(lab) {
		return nil, ErrInvalidLab
	}
	resp, err := api.Get(ctx, c, "/v1/kits/"+lab, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, api.Failure(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var body struct {
		Lab     string `json:"lab"`
		Version int64  `json:"version"`
		SHA256  string `json:"kit_sha256"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Lab != lab || body.Version < 1 || body.Version >= maxVersionNumber ||
		!reSHA256.MatchString(body.SHA256) {
		return nil, api.ErrBadResponse
	}
	return &Info{Lab: body.Lab, Version: int(body.Version), SHA256: body.SHA256}, nil
}

// Download fetches version of lab into a temporary file of dir and returns
// it, open and rewound: Extract reads that same file, which is never opened
// again by name between the check and the extraction. The SHA-256 of the bytes received must equal the header
// X-Sagnac-Kit-Sha256 and every non-empty value of expected (kit_sha256 of
// the current version); otherwise the file is removed and ErrChecksum
// returned. The kit must then carry a signature of lab, version and its
// content by a pinned key (signature.go); otherwise the file is removed and
// ErrSignature returned. The caller ends with Discard.
func Download(ctx context.Context, c *account.Client, lab string, version int, dir string, expected ...string) (*os.File, error) {
	if !ValidLab(lab) {
		return nil, ErrInvalidLab
	}
	// The default client gives 30 s to the whole exchange; a kit on a slow
	// line needs more. Same client otherwise: no redirect.
	hc := *c.HTTPClient()
	hc.Timeout = downloadTimeout
	resp, err := api.Get(ctx, c, "/v1/kits/"+lab+"/"+strconv.Itoa(version), &hc, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, api.Failure(resp)
	}
	announced := resp.Header.Get(headerSHA256)
	if !reSHA256.MatchString(announced) {
		return nil, ErrChecksum
	}
	for _, e := range expected {
		if e != "" && e != announced {
			return nil, ErrChecksum
		}
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/gzip") {
		return nil, api.ErrBadResponse
	}
	keyID, signature := resp.Header.Get(headerKeyID), resp.Header.Get(headerSignature)
	if keyID == "" || signature == "" {
		return nil, ErrSignature
	}
	tmp, err := os.CreateTemp(dir, ".sagnac-kit-*.tar.gz")
	if err != nil {
		return nil, writeFailed(err)
	}
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, MaxArchive+1))
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return nil, writeFailed(err)
	}
	if err != nil {
		return nil, err
	}
	if n > MaxArchive {
		return nil, &UnsafeError{Reason: reasonTooLarge}
	}
	if hex.EncodeToString(h.Sum(nil)) != announced {
		return nil, ErrChecksum
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, writeFailed(err)
	}
	tarSum, err := TarDigest(tmp, maxTar())
	var unsafeErr *UnsafeError
	if errors.As(err, &unsafeErr) {
		return nil, err
	}
	if err != nil || verifySignature(lab, version, tarSum, keyID, signature) != nil {
		return nil, ErrSignature
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, writeFailed(err)
	}
	keep = true
	return tmp, nil
}

// Discard closes and removes a file given by Download.
func Discard(f *os.File) {
	if f != nil {
		f.Close()
		os.Remove(f.Name())
	}
}
