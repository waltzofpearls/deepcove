package session

// The shared machine is sized against the machine it runs on: a laptop with 16 GiB should not be handed
// the same VM as a workstation with 128. A session's own VM keeps its fixed default instead, because you
// run several of those at once and the host has to hold them all.

// Bounds on the derived size. Not policy: below the minimum a coding agent cannot run, and above the
// maximum you are sizing for a workload no shared machine has had yet.
const (
	minWorkspaceCPU      = 2
	maxWorkspaceCPU      = 8
	minWorkspaceMemoryMB = 4 << 10   // 4 GiB
	maxWorkspaceMemoryMB = 16 << 10  // 16 GiB
	minWorkspaceDiskMB   = 16 << 10  // 16 GiB
	maxWorkspaceDiskMB   = 256 << 10 // 256 GiB, sparse, so it costs what gets written
)

// hostResources is what this machine has to give, as far as we can measure it. Zeroes mean the platform
// could not tell us, and the caller falls back to the minimums.
type hostResources struct {
	cores      int
	memoryMB   int64
	freeDiskMB int64
}

// WorkspaceDefaults sizes a new workspace VM against the host: half its cores, a quarter of its memory,
// and a quarter of the free space where deepcove keeps its data.
//
// Fractions rather than a fixed size because the machine is shared: it holds every session you put on
// it, and how many that is depends on the box.
func WorkspaceDefaults(dataDir string) Resources {
	host := hostSize(dataDir)
	return Resources{
		CPU:      float64(clampInt(host.cores/2, minWorkspaceCPU, maxWorkspaceCPU)),
		MemoryMB: clampMB(host.memoryMB/4, minWorkspaceMemoryMB, maxWorkspaceMemoryMB),
		// Whole GiB: the field is in GiB, and 120.56 is noise nobody wants to read or type.
		DiskMB: roundToGiB(clampMB(host.freeDiskMB/4, minWorkspaceDiskMB, maxWorkspaceDiskMB)),
	}
}

// roundToGiB rounds a size in MiB to the nearest whole GiB, never below one.
func roundToGiB(mb int64) int64 {
	if mb < 1024 {
		return 1024
	}
	return ((mb + 512) / 1024) * 1024
}

func clampInt(v, lo, hi int) int {
	return int(clampMB(int64(v), int64(lo), int64(hi)))
}

func clampMB(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
