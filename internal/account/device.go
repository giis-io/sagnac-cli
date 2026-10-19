package account

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var reDeviceID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// DeviceID returns the identifier of this machine, a UUID v4 kept in
// $XDG_CONFIG_HOME/sagnac/device_id (0600). The server uses it to count the
// devices of an account and to replace the tokens of a device that logs in
// again. A missing or damaged file is replaced; two commands started at the
// same time keep the first identifier written.
func DeviceID() (string, error) {
	dir, err := ensureConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "device_id")
	for range 3 {
		if raw, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(raw)); reDeviceID.MatchString(id) {
				return id, nil
			}
			// Damaged: put it aside, then write a new one below.
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		id, err := newUUID()
		if err != nil {
			return "", err
		}
		// Written aside then linked: the file appears complete or not at all,
		// and link fails if another command was faster.
		tmp, err := writeTemp(dir, "device_id", []byte(id+"\n"))
		if err != nil {
			return "", err
		}
		err = os.Link(tmp, path)
		_ = os.Remove(tmp)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", os.ErrExist
}

// SessionDeviceID returns a random identifier for one process (--session):
// it is never written, so the machine gets a new device at each run, which
// the command releases when it ends.
func SessionDeviceID() (string, error) { return newUUID() }

// DeviceName returns "<system>-<architecture>", never the host name
// (constraint of the plan; the server accepts only this pattern).
func DeviceName() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 9562
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// writeTemp writes data to a new file of dir (os.CreateTemp: mode 0600),
// synced to disk, and returns its path.
func writeTemp(dir, prefix string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, "."+prefix+"-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
