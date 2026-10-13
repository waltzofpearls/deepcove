package session

import "fmt"

// VMSize is a named size a VM can have. A session stores the name, so a size can gain something later,
// a disk for instance, and the sessions already made get it too.
type VMSize struct {
	Name     string `json:"name"`
	CPU      int    `json:"cpu"`
	MemoryMB int64  `json:"memory_mb"`
}

const (
	VMSizeSmall  = "small"
	VMSizeMedium = "medium"
	VMSizeLarge  = "large"
)

// DefaultVMSize is what a new VM gets unless you pick another size. It suits a Mac with 32 or 64 GB.
const DefaultVMSize = VMSizeMedium

// SessionVMSizes are the sizes a session's own VM can have. Small exists for a laptop with 16 GB.
var SessionVMSizes = []VMSize{
	{Name: VMSizeSmall, CPU: 1, MemoryMB: 1 << 10},
	{Name: VMSizeMedium, CPU: 1, MemoryMB: 2 << 10},
	{Name: VMSizeLarge, CPU: 2, MemoryMB: 4 << 10},
}

// workspaceVMSizeShares says how much of the host's spare memory each workspace VM size takes, and the
// most it takes. A machine with plenty of memory gets 4, 8 and 16 GB.
var workspaceVMSizeShares = []struct {
	name    string
	divisor int64
	maxGB   int64
}{
	{VMSizeSmall, 4, 4},
	{VMSizeMedium, 2, 8},
	{VMSizeLarge, 1, 16},
}

// smallestScaledWorkspaceVMGB drops a scaled size too small to be worth a choice of its own.
const smallestScaledWorkspaceVMGB = 2

// hostWorkspaceVMSizes are the workspace VM sizes this host can have.
func hostWorkspaceVMSizes() []VMSize {
	return workspaceVMSizes(hostMemoryMB(), hostMemoryKeptBackMB)
}

// workspaceVMSizes are the sizes the workspace VM can have on a host with hostMemoryMB, scaled to what
// the host can spare. Under 1 GB there is none, and up to 2 GB there is one. Unknown memory gets the
// full sizes.
func workspaceVMSizes(hostMemoryMB, keptBackMB int64) []VMSize {
	if hostMemoryMB <= 0 {
		var sizes []VMSize
		for _, share := range workspaceVMSizeShares {
			sizes = append(sizes, workspaceVMSize(share.name, share.maxGB))
		}
		return sizes
	}
	if hostMemoryMB < 1<<10 {
		return nil
	}
	spareMB := hostMemoryMB - keptBackMB
	onlySize := []VMSize{workspaceVMSize(VMSizeSmall, max(spareMB>>10, 1))}
	if hostMemoryMB < 2<<10 {
		return onlySize
	}
	var sizes []VMSize
	for _, share := range workspaceVMSizeShares {
		memoryGB := min((spareMB/share.divisor)>>10, share.maxGB)
		if memoryGB < smallestScaledWorkspaceVMGB {
			continue
		}
		sizes = append(sizes, workspaceVMSize(share.name, memoryGB))
	}
	if len(sizes) == 0 {
		return onlySize
	}
	return sizes
}

// workspaceVMSize gives a size 1 CPU for every 2 GB of memory.
func workspaceVMSize(name string, memoryGB int64) VMSize {
	return VMSize{Name: name, CPU: int(max(memoryGB/2, 1)), MemoryMB: memoryGB << 10}
}

// defaultWorkspaceVMSize is the usual default when the host has it, else the biggest size it has.
func defaultWorkspaceVMSize(sizes []VMSize) string {
	if _, ok := findVMSize(sizes, DefaultVMSize); ok || len(sizes) == 0 {
		return DefaultVMSize
	}
	return sizes[len(sizes)-1].Name
}

// SessionVMSizeChoice is a session VM size with how many of it fit on this host. FitsOnThisHost is
// absent when the host's memory could not be measured.
type SessionVMSizeChoice struct {
	VMSize
	FitsOnThisHost *int `json:"fits_on_this_host,omitempty"`
}

// WorkspaceVMSizeChoice is a workspace VM size with about how many agents it runs.
type WorkspaceVMSizeChoice struct {
	VMSize
	AgentsItRuns int `json:"agents_it_runs"`
}

func sessionVMSizeChoices(hostMemoryMB int64) []SessionVMSizeChoice {
	choices := make([]SessionVMSizeChoice, 0, len(SessionVMSizes))
	for _, size := range SessionVMSizes {
		choice := SessionVMSizeChoice{VMSize: size}
		if hostMemoryMB > 0 {
			fits := sessionVMsThatFit(size, hostMemoryMB, hostMemoryKeptBackMB)
			choice.FitsOnThisHost = &fits
		}
		choices = append(choices, choice)
	}
	return choices
}

func workspaceVMSizeChoices(sizes []VMSize) []WorkspaceVMSizeChoice {
	choices := make([]WorkspaceVMSizeChoice, 0, len(sizes))
	for _, size := range sizes {
		choices = append(choices, WorkspaceVMSizeChoice{VMSize: size, AgentsItRuns: agentsThatFit(size)})
	}
	return choices
}

func sessionVMsThatFit(size VMSize, hostMemoryMB, keptBackMB int64) int {
	spareMB := hostMemoryMB - keptBackMB
	if spareMB <= 0 {
		return 0
	}
	return int(spareMB / size.MemoryMB)
}

// agentsThatFit is about one agent per GB. Claude Code uses about 700 MB, and the rest is the VM's own.
func agentsThatFit(size VMSize) int {
	return int(max(size.MemoryMB>>10, 1))
}

func findVMSize(sizes []VMSize, name string) (VMSize, bool) {
	for _, size := range sizes {
		if size.Name == name {
			return size, true
		}
	}
	return VMSize{}, false
}

// validateVMSize refuses a size name that is not one of sizes, and a size sent together with numbers.
func validateVMSize(resources Resources, sizes []VMSize) error {
	if resources.Size == "" {
		return nil
	}
	if resources.CPU != 0 || resources.MemoryMB != 0 || resources.DiskMB != 0 {
		return fmt.Errorf("%w: give a size, or cpu and memory, not both", ErrInvalidInput)
	}
	if _, ok := findVMSize(sizes, resources.Size); !ok {
		return fmt.Errorf("%w: there is no VM size called %q", ErrInvalidInput, resources.Size)
	}
	return nil
}

// withSizeApplied fills in the CPU and memory of the named size, or returns resources unchanged when
// no size is named.
func (r Resources) withSizeApplied(sizes []VMSize) Resources {
	size, ok := findVMSize(sizes, r.Size)
	if !ok {
		return r
	}
	r.CPU, r.MemoryMB = float64(size.CPU), size.MemoryMB
	return r
}
