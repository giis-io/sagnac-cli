package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	key1 = "0b3f1c9e-4a7d-4f2b-9c1e-2d3f4a5b6c7d"
	key2 = "1c4a2d8f-5b6e-4a3c-8d2f-3e4a5b6c7d8e"
	// login is the connection that produced the entries.
	login = "3e6c4f0b-7d8a-4c5e-8f4b-5a6c7d8e9f0a"
)

func newQueue(t *testing.T, now time.Time) *Queue {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	q, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now }
	return q
}

func entry(key string) Entry {
	return Entry{Key: key, API: "https://sagnac.example", Login: login, Lab: "hello", Version: 1,
		Result: json.RawMessage(`{"schema":"sagnac-result/2","session":"local"}`)}
}

func TestDirIsUnderTheStateHome(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if got, err := Dir(); err != nil || got != filepath.Join(state, "sagnac", "queue") {
		t.Errorf("Dir = %q, %v", got, err)
	}
}

// An entry is the result, the key, the lab and its version, the address of
// the server, the connection and the date: nothing else, never a token. Directory 0700, file
// 0600, no temporary file left.
func TestAddWritesOnlyTheEntry(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	q := newQueue(t, now)
	if err := q.Add(entry(key1)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(q.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("queue directory: %v %v", info, err)
	}
	names, _ := filepath.Glob(filepath.Join(q.Dir, "*"))
	if len(names) != 1 || filepath.Base(names[0]) != key1+".json" {
		t.Fatalf("files %v", names)
	}
	info, _ = os.Stat(names[0])
	if info.Mode().Perm() != 0o600 {
		t.Errorf("entry mode %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(names[0])
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"api", "key", "lab", "login", "queued_at", "result", "version"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("fields %v, want %v", keys, want)
	}
	got, err := q.Pending()
	if err != nil || len(got) != 1 || got[0].Key != key1 || !got[0].QueuedAt.Equal(now) ||
		got[0].Login != login || string(got[0].Result) != string(entry(key1).Result) {
		t.Fatalf("Pending = %+v, %v", got, err)
	}
}

// A directory left with wider rights is brought back to 0700.
func TestAddTightensExistingDirectory(t *testing.T) {
	q := newQueue(t, time.Now())
	if err := os.MkdirAll(q.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(q.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := q.Add(entry(key1)); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(q.Dir); info.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}

func TestAddRefusesBadKey(t *testing.T) {
	q := newQueue(t, time.Now())
	for _, k := range []string{"", "../x", "not-a-uuid", key1 + "/x"} {
		e := entry(k)
		if err := q.Add(e); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}

// An entry always says which connection produced it.
func TestAddRefusesEntryWithoutLogin(t *testing.T) {
	q := newQueue(t, time.Now())
	for _, l := range []string{"", "not-a-uuid"} {
		e := entry(key1)
		e.Login = l
		if err := q.Add(e); err == nil {
			t.Errorf("login %q accepted", l)
		}
	}
}

// An entry of the format before the connection identifier (no "login") is
// deleted unsent: nothing says which account produced it.
func TestPendingDeletesEntryOfTheOldFormat(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	q := newQueue(t, now)
	if err := q.Add(entry(key1)); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(q.Dir, key2+".json")
	raw := `{"key":"` + key2 + `","api":"https://sagnac.example","lab":"hello","version":1,` +
		`"queued_at":"2026-10-19T09:00:00Z","result":{"schema":"sagnac-result/2","session":"local"}}`
	if err := os.WriteFile(old, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := q.Pending()
	if err != nil || len(got) != 1 || got[0].Key != key1 {
		t.Fatalf("Pending = %+v, %v", got, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("entry of the old format kept")
	}
}

func TestQueueExpires30Days(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	q := newQueue(t, start)
	if err := q.Add(entry(key1)); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return start.Add(29 * 24 * time.Hour) }
	if err := q.Add(entry(key2)); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return start.Add(30*24*time.Hour + time.Second) }
	got, err := q.Pending()
	if err != nil || len(got) != 1 || got[0].Key != key2 {
		t.Fatalf("Pending after 30 days = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(q.Dir, key1+".json")); !os.IsNotExist(err) {
		t.Error("expired entry still on disk")
	}
}

// Pending gives the entries oldest first, removes those it cannot read and
// ignores files that are not entries.
func TestPendingOrderAndDamage(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	q := newQueue(t, start.Add(time.Hour))
	if err := q.Add(entry(key2)); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return start }
	if err := q.Add(entry(key1)); err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(q.Dir, "2d5b3e9a-6c7f-4b4d-9e3a-4f5b6c7d8e9f.json")
	if err := os.WriteFile(damaged, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(q.Dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return start.Add(2 * time.Hour) }
	got, err := q.Pending()
	if err != nil || len(got) != 2 || got[0].Key != key1 || got[1].Key != key2 {
		t.Fatalf("Pending = %+v, %v", got, err)
	}
	if _, err := os.Stat(damaged); !os.IsNotExist(err) {
		t.Error("damaged entry kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("foreign file removed")
	}
}

func TestRemoveAndPurge(t *testing.T) {
	q := newQueue(t, time.Now())
	if n, err := q.Purge(); n != 0 || err != nil {
		t.Fatalf("Purge of nothing = %d, %v", n, err)
	}
	for _, k := range []string{key1, key2} {
		if err := q.Add(entry(k)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Remove(key1); err != nil {
		t.Fatal(err)
	}
	if err := q.Remove(key1); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	if n, err := q.Purge(); n != 1 || err != nil {
		t.Fatalf("Purge = %d, %v", n, err)
	}
	if got, _ := q.Pending(); len(got) != 0 {
		t.Errorf("after Purge: %+v", got)
	}
}

// An entry larger than 256 KiB (the limit of the server) or dated more than
// a day in the future is unreadable: deleted, never sent.
func TestPendingRejectsOversizedAndFutureEntries(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	q := newQueue(t, now)
	if err := q.Add(entry(key1)); err != nil {
		t.Fatal(err)
	}
	big := entry(key2)
	big.Result = json.RawMessage(`"` + strings.Repeat("x", MaxEntry) + `"`)
	if err := q.Add(big); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now.Add(25 * time.Hour) }
	future := entry("2d5b3e9a-6c7f-4b4d-9e3a-4f5b6c7d8e9f")
	if err := q.Add(future); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now.Add(time.Minute) }
	got, err := q.Pending()
	var keys []string
	for _, e := range got {
		keys = append(keys, e.Key)
	}
	if err != nil || len(got) != 1 || got[0].Key != key1 {
		t.Fatalf("Pending keys = %v, %v", keys, err)
	}
	for _, k := range []string{key2, future.Key} {
		if _, err := os.Stat(filepath.Join(q.Dir, k+".json")); !os.IsNotExist(err) {
			t.Errorf("%s kept", k)
		}
	}
}
