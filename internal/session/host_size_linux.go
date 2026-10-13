package session

import (
	"os"
	"strconv"
	"strings"
)

// hostMemoryKeptBackMB is left for a Linux host: about 600 MB for the system and deepcove on a server,
// and the rest for each VM's helpers.
const hostMemoryKeptBackMB = 1 << 10

// hostMemoryMB reads the machine's physical memory from /proc/meminfo, whose MemTotal is in kB.
func hostMemoryMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(data)) {
		total, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(total)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb >> 10
	}
	return 0
}
