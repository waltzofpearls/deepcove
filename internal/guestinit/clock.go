package guestinit

import (
	"strconv"
	"strings"
	"time"
)

// The hardware clock counts whole seconds, so a gap under 2 seconds may be rounding and is left alone.
const clockDriftLimit = 2 * time.Second

// parseHardwareClock reads the seconds since 1970 that the kernel reports for the hardware clock.
func parseHardwareClock(raw []byte) (time.Time, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0), nil
}

func clockStepNeeded(guest, hardware time.Time) bool {
	gap := hardware.Sub(guest)
	return gap >= clockDriftLimit || gap <= -clockDriftLimit
}
