package account

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

const lockPoll = 50 * time.Millisecond

// LockFile takes an exclusive flock(2) on path (created 0600 if needed) and
// returns the function that releases it. It waits for the lock until ctx is
// done. The kernel releases the lock if the process dies, so a crashed CLI
// never blocks the next one.
func LockFile(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
