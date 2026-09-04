package session

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// usableCores counts the performance cores. Apple Silicon reports efficiency cores too, and sizing a VM
// from the total promises speed the machine will not give.
func usableCores() int {
	if n, err := unix.SysctlUint32("hw.perflevel0.logicalcpu"); err == nil && n > 0 {
		return int(n)
	}
	return runtime.NumCPU() // Intel Macs have one performance level.
}
