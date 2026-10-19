// Package instructions fetches the instructions of a lab (GET
// /v1/instructions/…), keeps an offline copy of them in the cache of the
// person and opens it. The token goes in the Authorization header only, through
// the client of the account package; the address that is opened is either a
// file of the cache or a page of the site the server named and the CLI checked.
package instructions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/kit"
	"github.com/giis-io/sagnac-cli/internal/xdg"
)

// CodeOfflineNotIncluded: the offer of the account does not include the
// offline copy (docs/api/erreurs.md); the answer carries the page of the site.
const CodeOfflineNotIncluded = "OFFLINE_INSTRUCTIONS_NOT_INCLUDED"

const codeForeignPage = "INSTRUCTIONS_FOREIGN_PAGE"

const (
	maxPage      = 8 << 20
	maxList      = 500
	maxMeta      = 64 << 10
	maxVersion   = 1 << 30
	codeCache    = "INSTRUCTIONS_CACHE_FAILED"
	cachePerm    = 0o600
	cacheDirPerm = 0o700
)

var (
	// ErrCache: the offline copy cannot be written or read (no cache
	// directory, rights, full disk).
	ErrCache = errors.New(codeCache)
	// ErrForeignPage: the address named by the server is not a page of its own
	// site; it is never opened.
	ErrForeignPage = errors.New(codeForeignPage)

	reModule = regexp.MustCompile(`^B[0-9]+-M[0-9]+$`)
	reQuery  = regexp.MustCompile(`^version=[0-9]{1,10}$`)
	rePrint  = regexp.MustCompile(`^[\x21-\x7e]{1,1000}$`)
	reCopy   = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,47})-v([0-9]{1,10})\.html$`)
)

// IsModule reports whether arg is the identifier of a module (B1-M2).
func IsModule(arg string) bool { return reModule.MatchString(arg) }

// Page is the instructions of one version of a lab, in both formats.
type Page struct {
	HTML []byte
	Text []byte
}

// LabVersion is one lab of a module and its current version.
type LabVersion struct {
	Lab     string
	Version int
}

func fetch(ctx context.Context, c *account.Client, path, wantType string) ([]byte, error) {
	resp, err := api.Get(ctx, c, path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, api.Failure(resp)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), wantType) {
		return nil, api.ErrBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPage+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxPage {
		return nil, api.ErrBadResponse
	}
	return raw, nil
}

// Fetch gets the instructions of version of lab, as a page and as text.
func Fetch(ctx context.Context, c *account.Client, lab string, version int) (*Page, error) {
	if !kit.ValidLab(lab) {
		return nil, kit.ErrInvalidLab
	}
	if version < 1 || version > maxVersion {
		return nil, api.ErrBadResponse
	}
	path := "/v1/instructions/" + lab + "?version=" + strconv.Itoa(version)
	html, err := fetch(ctx, c, path, "text/html")
	if err != nil {
		return nil, err
	}
	text, err := fetch(ctx, c, path+"&format=text", "text/plain")
	if err != nil {
		return nil, err
	}
	return &Page{HTML: html, Text: text}, nil
}

// ModuleLabs lists the labs of a module with their current versions.
func ModuleLabs(ctx context.Context, c *account.Client, module string) ([]LabVersion, error) {
	if !IsModule(module) {
		return nil, kit.ErrInvalidLab
	}
	raw, err := fetch(ctx, c, "/v1/instructions?module="+url.QueryEscape(module), "application/json")
	if err != nil {
		return nil, err
	}
	var body struct {
		Labs []struct {
			Lab     string `json:"lab"`
			Version int64  `json:"version"`
		} `json:"labs"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Labs) > maxList {
		return nil, api.ErrBadResponse
	}
	out := make([]LabVersion, 0, len(body.Labs))
	for _, l := range body.Labs {
		if !kit.ValidLab(l.Lab) || l.Version < 1 || l.Version > maxVersion {
			return nil, api.ErrBadResponse
		}
		out = append(out, LabVersion{Lab: l.Lab, Version: int(l.Version)})
	}
	return out, nil
}

// SafePageURL returns raw when it is the instructions page of lab on the
// origin of api: same scheme and host (api is https, or http on the loopback,
// already checked), path /labs/<lab>/enonce, at most "?version=N", no user
// information, fragment or escape, printable ASCII. Anything else is
// ErrForeignPage: the server's text is never opened as it is.
func SafePageURL(apiOrigin, lab, raw string) (string, error) {
	if !rePrint.MatchString(raw) || strings.Contains(raw, "%") {
		return "", ErrForeignPage
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.ForceQuery ||
		!strings.EqualFold(u.Scheme+"://"+u.Host, apiOrigin) ||
		u.Path != "/labs/"+lab+"/enonce" || (u.RawQuery != "" && !reQuery.MatchString(u.RawQuery)) {
		return "", ErrForeignPage
	}
	return raw, nil
}

// InstalledVersion reads the version of lab in the meta.yaml of the kit
// installed in start or above it (<dir>/<lab>/meta.yaml, or meta.yaml of the
// lab directory itself). A file that is not plain, too big, of another lab or
// without a version is ignored.
func InstalledVersion(start, lab string) (int, bool) {
	for dir := start; ; {
		candidates := []string{filepath.Join(dir, lab, "meta.yaml")}
		if filepath.Base(dir) == lab {
			candidates = append(candidates, filepath.Join(dir, "meta.yaml"))
		}
		for _, p := range candidates {
			if v, ok := metaVersion(p, lab); ok {
				return v, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return 0, false
		}
		dir = parent
	}
}

func metaVersion(path, lab string) (int, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMeta {
		return 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	version, id := 0, lab
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if !found || key != strings.TrimSpace(key) || key == "" {
			continue // indented lines belong to a nested key
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "id":
			id = value
		case "version":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > maxVersion {
				return 0, false
			}
			version = n
		}
	}
	return version, version != 0 && id == lab
}

// CacheDir returns $XDG_CACHE_HOME/sagnac/instructions, or
// ~/.cache/sagnac/instructions when the variable is empty or relative. It
// does not create it.
func CacheDir() (string, error) {
	base, err := xdg.CacheHome()
	if err != nil {
		return "", ErrCache
	}
	return filepath.Join(base, "sagnac", "instructions"), nil
}

// Purge deletes the offline copies of every lab. It is called when the
// connection ends or changes (logout, refused tokens, new login): the copies
// belong to the account that fetched them. No cache directory is not an error.
func Purge() error {
	dir, err := CacheDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return ErrCache
	}
	return nil
}

// Store writes the offline copy of version of lab: <lab>-v<version>.html and
// .txt in the cache directory (0700), each file replaced atomically (0600).
// It returns the two paths.
func Store(lab string, version int, p *Page) (htmlPath, textPath string, err error) {
	if !kit.ValidLab(lab) || version < 1 || version > maxVersion {
		return "", "", kit.ErrInvalidLab
	}
	dir, err := CacheDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return "", "", ErrCache
	}
	if err := os.Chmod(dir, cacheDirPerm); err != nil {
		return "", "", ErrCache
	}
	base := filepath.Join(dir, lab+"-v"+strconv.Itoa(version))
	htmlPath, textPath = base+".html", base+".txt"
	if err := writeAtomic(dir, htmlPath, p.HTML); err != nil {
		return "", "", err
	}
	if err := writeAtomic(dir, textPath, p.Text); err != nil {
		return "", "", err
	}
	return htmlPath, textPath, nil
}

func writeAtomic(dir, target string, data []byte) (err error) {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return ErrCache
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(cachePerm); err != nil {
		return ErrCache
	}
	if _, err = f.Write(data); err != nil {
		return ErrCache
	}
	if err = f.Sync(); err != nil {
		return ErrCache
	}
	if err = f.Close(); err != nil {
		return ErrCache
	}
	if err = os.Rename(f.Name(), target); err != nil {
		return ErrCache
	}
	return nil
}

// Local finds the offline copy of lab: of version, or of the highest version
// when version is 0. ok is false when there is none (or when it is not a
// plain file). The text file is "" when only the page exists.
func Local(lab string, version int) (v int, htmlPath, textPath string, ok bool) {
	dir, err := CacheDir()
	if err != nil || !kit.ValidLab(lab) {
		return 0, "", "", false
	}
	if version == 0 {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return 0, "", "", false
		}
		for _, e := range entries {
			m := reCopy.FindStringSubmatch(e.Name())
			if m == nil || m[1] != lab {
				continue
			}
			if n, err := strconv.Atoi(m[2]); err == nil && n > version {
				version = n
			}
		}
		if version == 0 {
			return 0, "", "", false
		}
	}
	base := filepath.Join(dir, lab+"-v"+strconv.Itoa(version))
	if !plain(base + ".html") {
		return 0, "", "", false
	}
	htmlPath = base + ".html"
	if plain(base + ".txt") {
		textPath = base + ".txt"
	}
	return version, htmlPath, textPath, true
}

func plain(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// ReadText reads a text copy for the terminal: control characters other than
// the line end and the tab are dropped, so that a text cannot drive the
// terminal.
func ReadText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrCache
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPage))
	if err != nil {
		return "", ErrCache
	}
	return Sanitize(string(raw)), nil
}

// Sanitize drops the control characters of s except "\n" and "\t".
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			return r
		}
		return -1
	}, strings.ReplaceAll(s, "\r\n", "\n"))
}
