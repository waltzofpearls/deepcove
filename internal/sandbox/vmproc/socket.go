package vmproc

import (
	"errors"
	"io/fs"
	"os"
)

// RemoveSocket removes a socket file, and is fine when it is already gone.
func RemoveSocket(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
