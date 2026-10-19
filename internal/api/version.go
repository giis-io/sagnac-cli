package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/giis-io/sagnac-cli/internal/account"
	"github.com/giis-io/sagnac-cli/internal/xdg"
)

// The static version file (README, section « Commandes »): {latest, minimum,
// security, advisory}. It is public: no token is ever sent to it, and the only
// thing the request carries is the version of the CLI (User-Agent).
const (
	versionFileName = "version.json"
	versionLockName = "version.lock"
	// A successful fetch is reused for a day; after a failure the next
	// automatic attempt waits an hour, so that an offline machine does not pay
	// the timeout at every launch.
	VersionMaxAge       = 24 * time.Hour
	versionRetryAfter   = time.Hour
	maxVersionBody      = 4 << 10
	versionDirPerm      = 0o700
	versionFilePerm     = 0o600
	codeVersionNoCache  = "API_VERSION_NO_CACHE_DIR"
	codeVersionResponse = "API_VERSION_BAD_FILE"
)

// VersionTimeout bounds the whole check; no command waits longer for it.
var VersionTimeout = 3 * time.Second

var (
	// ErrVersionFile: the file is not what the contract describes.
	ErrVersionFile = errors.New(codeVersionResponse)
	// ErrNoCacheDir: neither XDG_CACHE_HOME nor HOME gives a directory.
	ErrNoCacheDir = errors.New(codeVersionNoCache)

	// The pattern of the server (tentatives/resultat.py, RE_VERSION) without
	// its optional "name/" prefix: a pre-release and build metadata of the
	// same alphabet and the same (absent) length bound. What is shown stays
	// bounded by the size of the file (maxVersionBody).
	reSemver   = regexp.MustCompile(`^v?([0-9]{1,9})\.([0-9]{1,9})\.([0-9]{1,9})(-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	reAdvisory = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)
)

// VersionInfo is the content of the version file, checked: the strings are
// versions and an advisory identifier of a fixed alphabet, safe to display.
type VersionInfo struct {
	Latest   string `json:"latest"`
	Minimum  string `json:"minimum"`
	Security bool   `json:"security"`
	Advisory string `json:"advisory"`
}

// Compare compares two versions by the rules of the server (version_semantique):
// build metadata (+...) is ignored, a pre-release is lower than the release
// of the same number, two pre-releases of the same number are equal. ok is
// false when one of them is not MAJ.MIN.CORR.
func Compare(a, b string) (cmp int, ok bool) {
	ka, oka := semverKey(a)
	kb, okb := semverKey(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			if ka[i] < kb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func semverKey(v string) ([4]int, bool) {
	m := reSemver.FindStringSubmatch(v)
	if m == nil {
		return [4]int{}, false
	}
	var k [4]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return k, false
		}
		k[i] = n
	}
	if m[4] == "" {
		k[3] = 1
	}
	return k, true
}

// parseVersionFile reads and checks the file. Unknown fields are ignored;
// a version of security needs an advisory identifier.
func parseVersionFile(raw []byte) (*VersionInfo, error) {
	var body struct {
		Latest   string  `json:"latest"`
		Minimum  string  `json:"minimum"`
		Security *bool   `json:"security"`
		Advisory *string `json:"advisory"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Security == nil {
		return nil, ErrVersionFile
	}
	info := &VersionInfo{Latest: body.Latest, Minimum: body.Minimum, Security: *body.Security}
	if body.Advisory != nil {
		info.Advisory = *body.Advisory
	}
	if _, ok := semverKey(info.Latest); !ok {
		return nil, ErrVersionFile
	}
	if _, ok := semverKey(info.Minimum); !ok {
		return nil, ErrVersionFile
	}
	if c, _ := Compare(info.Minimum, info.Latest); c > 0 {
		return nil, ErrVersionFile
	}
	if info.Advisory != "" && !reAdvisory.MatchString(info.Advisory) {
		return nil, ErrVersionFile
	}
	if info.Security && info.Advisory == "" {
		return nil, ErrVersionFile
	}
	return info, nil
}

// VersionCacheDir returns $XDG_CACHE_HOME/sagnac, or ~/.cache/sagnac.
func VersionCacheDir() (string, error) {
	base, err := xdg.CacheHome()
	if err != nil {
		return "", ErrNoCacheDir
	}
	return filepath.Join(base, "sagnac"), nil
}

type versionCache struct {
	URL       string       `json:"url"`
	FetchedAt time.Time    `json:"fetched_at"`
	AttemptAt time.Time    `json:"attempt_at"`
	Info      *VersionInfo `json:"info,omitempty"`
}

// VersionStatus is the outcome of LoadVersion.
type VersionStatus struct {
	// Info is the last known content, fresh or not; nil when there is none.
	Info *VersionInfo
	// Checked is the time of the last successful fetch (zero when none).
	Checked time.Time
	// Err is set when a fetch was tried and failed (Info may still be the
	// older copy).
	Err error
}

// LoadVersion returns the version file, from the daily cache when it is fresh
// and from the network otherwise (at most once a day). A failed fetch is
// recorded, so that automatic checks wait an hour before trying again; force
// ignores that wait (the doctor command). Nothing here blocks longer than
// VersionTimeout, the wait for the lock included, and nothing is returned as
// an error the caller must act on.
//
// The fetch is made under a file lock of the cache directory, and the cache
// is read again once the lock is held: two commands launched together fetch
// the file once.
func LoadVersion(ctx context.Context, url, cliVersion string, now time.Time, force bool) VersionStatus {
	dir, dirErr := VersionCacheDir()
	st, cached := cachedVersion(dir, dirErr, url)
	if !due(cached, now, force) {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, VersionTimeout)
	defer cancel()
	if dirErr == nil && os.MkdirAll(dir, versionDirPerm) == nil {
		unlock, err := account.LockFile(ctx, filepath.Join(dir, versionLockName))
		if err == nil {
			defer unlock()
			if st, cached = cachedVersion(dir, dirErr, url); !due(cached, now, force) {
				return st
			}
		} else if ctx.Err() != nil {
			return st // another command is still fetching: use the copy we have
		}
	}
	info, err := fetchVersion(ctx, url, cliVersion)
	if err == nil {
		cached = versionCache{URL: url, FetchedAt: now, AttemptAt: now, Info: info}
		st = VersionStatus{Info: info, Checked: now}
	} else {
		cached.URL, cached.AttemptAt = url, now
		st.Err = err
	}
	if dirErr == nil {
		_ = writeVersionCache(dir, cached) // a cache that cannot be written is not an error
	}
	return st
}

// CachedVersion returns what the cache holds, with no network and no lock:
// the commands that must answer at once (help, unknown command) use it.
func CachedVersion(url string) VersionStatus {
	dir, dirErr := VersionCacheDir()
	st, _ := cachedVersion(dir, dirErr, url)
	return st
}

func cachedVersion(dir string, dirErr error, url string) (VersionStatus, versionCache) {
	var cached versionCache
	if dirErr == nil {
		cached = readVersionCache(filepath.Join(dir, versionFileName), url)
	}
	return VersionStatus{Info: cached.Info, Checked: cached.FetchedAt}, cached
}

// due: the file has to be fetched now.
func due(cached versionCache, now time.Time, force bool) bool {
	if cached.Info != nil && fresh(cached.FetchedAt, now, VersionMaxAge) {
		return false
	}
	return force || !fresh(cached.AttemptAt, now, versionRetryAfter)
}

func fresh(t, now time.Time, age time.Duration) bool {
	return !t.IsZero() && !t.After(now.Add(time.Minute)) && now.Sub(t) < age
}

func readVersionCache(path, url string) versionCache {
	var c versionCache
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxVersionBody*2 {
		return versionCache{}
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.URL != url {
		return versionCache{}
	}
	if c.Info != nil {
		// Re-checked: the cache is a file of the user's account, not a trusted source.
		again, _ := json.Marshal(c.Info)
		info, err := parseVersionFile(again)
		if err != nil {
			return versionCache{}
		}
		c.Info = info
	}
	return c
}

func writeVersionCache(dir string, c versionCache) (err error) {
	if err = os.MkdirAll(dir, versionDirPerm); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".version-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(versionFilePerm); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, versionFileName))
}

func fetchVersion(ctx context.Context, url, cliVersion string) (*VersionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, VersionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sagnac/"+cliVersion)
	hc := &http.Client{
		Timeout:       VersionTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrVersionFile
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxVersionBody))
	if err != nil {
		return nil, err
	}
	return parseVersionFile(raw)
}
