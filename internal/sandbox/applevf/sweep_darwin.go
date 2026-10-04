//go:build darwin

package applevf

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// pidFileLocked reports whether a live `vm run` still holds the lock on its PID file.
func pidFileLocked(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	return errors.Is(unix.Flock(int(file.Fd()), unix.LOCK_SH|unix.LOCK_NB), unix.EWOULDBLOCK)
}
