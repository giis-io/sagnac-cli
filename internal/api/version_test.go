package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const goodFile = `{"latest":"1.2.4","minimum":"1.0.0","security":false,"advisory":null}`

// Two commands launched together read the version file once: the second waits
// for the lock, then finds the copy of the first.
func TestVersionFileFetchedOnceByParallelCommands(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte(goodFile))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	results := make([]VersionStatus, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = LoadVersion(context.Background(), srv.URL, "1.2.3", now, false)
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Errorf("version file read %d times, want 1", n)
	}
	for i, st := range results {
		if st.Info == nil || st.Info.Latest != "1.2.4" {
			t.Errorf("command %d got %+v", i, st)
		}
	}
}

// A command that waits for the lock longer than the timeout goes on with the
// copy it has; it does not wait for ever.
func TestVersionLockWaitIsBounded(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	old := VersionTimeout
	VersionTimeout = 200 * time.Millisecond
	defer func() { VersionTimeout = old }()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	go LoadVersion(context.Background(), srv.URL, "1.2.3", now, false)
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	st := LoadVersion(context.Background(), srv.URL, "1.2.3", now, false)
	if d := time.Since(start); d > time.Second || st.Info != nil {
		t.Errorf("waited %v, info %+v", d, st.Info)
	}
}

// The bounds are those of the server (RE_VERSION): a long pre-release or build
// metadata that the server accepts is accepted here.
func TestVersionPatternIsTheServers(t *testing.T) {
	long := strings.Repeat("a", 80)
	for _, v := range []string{"1.2.3-" + long, "1.2.3+" + long, "1.2.3-rc.1+build-5.x", "v1.2.3", "1.2.3-0"} {
		if _, ok := semverKey(v); !ok {
			t.Errorf("%q refused", v)
		}
	}
	for _, v := range []string{"1.2", "1.2.3-", "1.2.3+", "1.2.3-é", "1.2.3 ", "1.2.3\n", "1234567890.0.0", "x/1.2.3"} {
		if _, ok := semverKey(v); ok {
			t.Errorf("%q accepted", v)
		}
	}
}
