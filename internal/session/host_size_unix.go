//go:build darwin || linux

package session

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// hostSize measures the machine. Every failure is a zero rather than an error: this only feeds a
// default, and a default nobody can compute is the minimum, not a reason to fail a page load.
func hostSize(dataDir string) hostResources {
	host := hostResources{cores: runtime.NumCPU()}
	host.memoryMB = hostMemoryMB()

	dir := dataDir
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	var fs unix.Statfs_t
	if dir != "" && unix.Statfs(dir, &fs) == nil {
		host.freeDiskMB = int64(fs.Bavail) * int64(fs.Bsize) >> 20
	}
	return host
}
