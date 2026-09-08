package cloudhv

import (
	"errors"
	"io/fs"
	"os"
)

func removeSocket(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
