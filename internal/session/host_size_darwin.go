package session

import "golang.org/x/sys/unix"

// hostMemoryKeptBackMB is left for the Mac itself: macOS, the apps you have open, deepcove.
const hostMemoryKeptBackMB = 4 << 10

// hostMemoryMB reads the machine's physical memory. hw.memsize is the byte count macOS reports for
// installed RAM.
func hostMemoryMB() int64 {
	bytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(bytes >> 20)
}
