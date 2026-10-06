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

// WorkspaceVMSizes are the sizes the workspace VM can have. It runs many agents at once, so its sizes
// are bigger.
var WorkspaceVMSizes = []VMSize{
	{Name: VMSizeSmall, CPU: 2, MemoryMB: 4 << 10},
	{Name: VMSizeMedium, CPU: 4, MemoryMB: 8 << 10},
	{Name: VMSizeLarge, CPU: 8, MemoryMB: 16 << 10},
}

const (
	// hostMemoryKeptBackMB is left for the host itself: macOS, the browser, deepcove.
	hostMemoryKeptBackMB = 4 << 10
	// workspaceSystemMemoryMB is what the workspace VM's own system uses before any agent starts.
	workspaceSystemMemoryMB = 1 << 10
	// memoryPerAgentMB is what one agent needs. Claude Code uses about 700 MB.
	memoryPerAgentMB = 1 << 10
)

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
			fits := sessionVMsThatFit(size, hostMemoryMB)
			choice.FitsOnThisHost = &fits
		}
		choices = append(choices, choice)
	}
	return choices
}

func workspaceVMSizeChoices() []WorkspaceVMSizeChoice {
	choices := make([]WorkspaceVMSizeChoice, 0, len(WorkspaceVMSizes))
	for _, size := range WorkspaceVMSizes {
		choices = append(choices, WorkspaceVMSizeChoice{VMSize: size, AgentsItRuns: agentsThatFit(size)})
	}
	return choices
}

func sessionVMsThatFit(size VMSize, hostMemoryMB int64) int {
	spareMB := hostMemoryMB - hostMemoryKeptBackMB
	if spareMB <= 0 {
		return 0
	}
	return int(spareMB / size.MemoryMB)
}

func agentsThatFit(size VMSize) int {
	return int((size.MemoryMB - workspaceSystemMemoryMB) / memoryPerAgentMB)
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
