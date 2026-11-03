// Package queue keeps the attempts that could not be sent (network down or
// server error) in $XDG_STATE_HOME/sagnac/queue/, one file per attempt, for
// 30 days. An entry holds the result, its idempotency key, the lab, the
// address of the server, the identifier of the connection that produced it
// and the date: never a token. The next "sagnac check" sends it again with
// the same key, so the server records it once, and only under the same
// connection.
package queue

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/giis-io/sagnac-cli/internal/xdg"
)

// MaxAge is the life of an entry; an older one is deleted unsent.
const MaxAge = 30 * 24 * time.Hour

// MaxEntry bounds the size of an entry read back: the server refuses a
// result over 48 KiB, so a larger file is not an attempt.
const MaxEntry = 48 << 10

// maxFuture: an entry dated further ahead than this was not written by this
// clock; it is deleted.
const maxFuture = 24 * time.Hour

var errTooLarge = errors.New(codeTooLarge)

// readBounded reads path, refusing more than MaxEntry bytes of result plus
// the few fields around it.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limit := int64(MaxEntry + 4096)
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errTooLarge
	}
	return raw, nil
}

const (
	suffix       = ".json"
	codeKey      = "QUEUE_INVALID_KEY"
	codeNoDir    = "QUEUE_NO_STATE_DIR"
	codeLink     = "QUEUE_NOT_A_DIRECTORY"
	codeTooLarge = "QUEUE_ENTRY_TOO_LARGE"
)

var (
	// ErrInvalidKey: the key is not a UUID; it would not name a file safely.
	ErrInvalidKey = errors.New(codeKey)
	// ErrNoStateDir: neither XDG_STATE_HOME nor HOME gives a directory.
	ErrNoStateDir = errors.New(codeNoDir)
	// ErrNotDirectory: the queue path exists and is not a directory (a
	// symbolic link included).
	ErrNotDirectory = errors.New(codeLink)

	reKey = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Entry is one attempt waiting to be sent.
type Entry struct {
	Key string `json:"key"`
	API string `json:"api"`
	// Login is the identifier of the connection of the device that produced
	// the entry (account.Tokens.Login). An entry without one comes from an
	// older version, which did not say whose it was: it is deleted unsent.
	Login    string          `json:"login"`
	Lab      string          `json:"lab"`
	Version  int64           `json:"version"`
	QueuedAt time.Time       `json:"queued_at"`
	Result   json.RawMessage `json:"result"`
}

// Queue is the directory of the entries. Now is the clock (tests).
type Queue struct {
	Dir string
	Now func() time.Time
}

// Dir returns $XDG_STATE_HOME/sagnac/queue, or ~/.local/state/sagnac/queue
// when the variable is empty or relative (XDG Base Directory).
func Dir() (string, error) {
	base, err := xdg.StateHome()
	if err != nil {
		return "", ErrNoStateDir
	}
	return filepath.Join(base, "sagnac", "queue"), nil
}

// Open returns the queue of this user. Nothing is created before an Add.
func Open() (*Queue, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return &Queue{Dir: dir, Now: time.Now}, nil
}

// ValidKey reports whether key can be an idempotency key of the queue.
func ValidKey(key string) bool { return reKey.MatchString(key) }

// Add writes the entry (QueuedAt set to now), atomically: a reader sees the
// whole file or nothing. The directory is 0700, the file 0600.
func (q *Queue) Add(e Entry) error {
	if !ValidKey(e.Key) || !ValidKey(e.Login) {
		return ErrInvalidKey
	}
	if err := q.ensureDir(); err != nil {
		return err
	}
	e.QueuedAt = q.Now().UTC()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// CreateTemp gives mode 0600.
	tmp, err := os.CreateTemp(q.Dir, ".entry-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(q.Dir, e.Key+suffix))
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

func (q *Queue) ensureDir() error {
	if err := os.MkdirAll(q.Dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(q.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrNotDirectory
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(q.Dir, 0o700)
	}
	return nil
}

// Pending returns the entries, oldest first. It deletes those older than
// MaxAge, those without a connection identifier and those it cannot read;
// files that are not entries are left.
func (q *Queue) Pending() ([]Entry, error) {
	keys, err := q.keys()
	if err != nil {
		return nil, err
	}
	now := q.Now()
	var out []Entry
	for _, key := range keys {
		path := filepath.Join(q.Dir, key+suffix)
		raw, err := readBounded(path)
		if errors.Is(err, errTooLarge) {
			_ = os.Remove(path)
			continue
		}
		if err != nil {
			continue
		}
		var e Entry
		if json.Unmarshal(raw, &e) != nil || e.Key != key || !ValidKey(e.Login) || len(e.Result) == 0 || len(e.Result) > MaxEntry || e.QueuedAt.IsZero() ||
			now.Sub(e.QueuedAt) > MaxAge || e.QueuedAt.Sub(now) > maxFuture {
			_ = os.Remove(path)
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].QueuedAt.Before(out[j].QueuedAt) })
	return out, nil
}

// keys lists the keys of the entry files; no directory is no entry.
func (q *Queue) keys() ([]string, error) {
	info, err := os.Lstat(q.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDirectory
	}
	dirents, err := os.ReadDir(q.Dir)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, d := range dirents {
		key, found := strings.CutSuffix(d.Name(), suffix)
		if found && d.Type().IsRegular() && ValidKey(key) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// Remove deletes the entry of key; a missing entry is not an error.
func (q *Queue) Remove(key string) error {
	if !ValidKey(key) {
		return ErrInvalidKey
	}
	err := os.Remove(filepath.Join(q.Dir, key+suffix))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Purge deletes every entry and returns how many there were.
func (q *Queue) Purge() (int, error) {
	keys, err := q.keys()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		if err := q.Remove(k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
