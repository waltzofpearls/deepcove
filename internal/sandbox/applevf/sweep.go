package applevf

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// SweepLeftovers kills the `vm run` processes a previous deepcove left behind under root, and removes
// their folders. Run it before the driver touches any disk a leftover VM may still be using.
func SweepLeftovers(root string) (int, error) {
	return sweepLeftovers(root, pidFileLocked, syscall.Kill)
}

func sweepLeftovers(root string, locked func(path string) bool, kill func(int, syscall.Signal) error) (int, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	stopped := 0
	var errs []error
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		path := filepath.Join(dir, "vm.pid")
		// Only a `vm run` still alive holds the lock. A PID with no lock behind it may be another program
		// by now.
		if pid, ok := readPID(path); ok && locked(path) {
			// A leftover was orphaned long ago and may be jammed, so it gets no polite stop.
			if err := kill(pid, syscall.SIGKILL); err == nil {
				stopped++
			}
		}
		errs = append(errs, os.RemoveAll(dir))
	}
	return stopped, errors.Join(errs...)
}

func readPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, err == nil && pid > 1
}
