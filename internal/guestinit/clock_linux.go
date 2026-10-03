//go:build linux

package guestinit

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	hardwareClockPath = "/sys/class/rtc/rtc0/since_epoch"
	clockCheckEvery   = 10 * time.Second
)

// followHardwareClock keeps the guest clock on the virtual hardware clock. The guest clock stops while
// the Mac sleeps, and the hardware clock does not.
func followHardwareClock() {
	for {
		time.Sleep(clockCheckEvery)
		raw, err := os.ReadFile(hardwareClockPath)
		if err != nil {
			continue
		}
		hardware, err := parseHardwareClock(raw)
		now := time.Now()
		if err != nil || !clockStepNeeded(now, hardware) {
			continue
		}
		timeval := unix.NsecToTimeval(hardware.UnixNano())
		if err := unix.Settimeofday(&timeval); err != nil {
			logf("WARNING: could not set the clock: %v", err)
			continue
		}
		logf("clock was off by %s from the hardware clock, set it", hardware.Sub(now).Round(time.Second))
	}
}
