// Package session manages the lifecycle of coding-agent sessions hosted on this node: the layer
// between the API surface and the sandbox driver.
//
// A Session is a row in the sessions table plus a live sandbox. The Service handles
// create/list/get/stop/destroy with the sandbox driver, persisting state in DuckDB so it survives a
// service restart.
//
// Owner identity and authorization are not wired in this slice: Service uses a placeholder owner;
// the real identity layer plugs in via request context once tsnet/WhoIs lands.
package session

import (
	"fmt"
	"time"
)

// Status enumerates the lifecycle states tracked in the sessions
// table. The state machine is small on purpose: created → running →
// stopped, with destroy removing the row outright. Failures
// transition to "errored" so they're visible in `session ps`.
type Status string

const (
	StatusCreated Status = "created"
	StatusRunning Status = "running"
	// StatusStarting is never stored. It is reported while a start is in flight, because a VM takes
	// long enough to boot that the row would otherwise read "stopped" for half a minute.
	StatusStarting Status = "starting"
	StatusStopped  Status = "stopped"
	StatusErrored  Status = "errored"
)

// StopReason says why deepcove stopped a session on its own.
type StopReason string

// StopReasonUnresponsive is a VM that stopped answering, so deepcove shut it down.
const StopReasonUnresponsive StopReason = "unresponsive"

// Mounts only holds USER-supplied mounts; the agent's home is an implementation detail and stays out of
// the API.
type Session struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Agent      string `json:"agent"`
	Image      string `json:"image"`
	OwnerEmail string `json:"owner_email"`
	Status     Status `json:"status"`
	// StopReason is empty unless deepcove stopped the session on its own.
	StopReason  StopReason        `json:"stop_reason,omitempty"`
	Env         map[string]string `json:"env"`
	Mounts      []MountRequest    `json:"mounts"`
	SetupScript string            `json:"setup_script"`
	// Runtime is the sandbox runtime hosting this session ("applevf", "cloudhv", "sandboxless"). Persisted
	// because a node can offer several, so stop, destroy and exec route back to the driver that made it.
	Runtime string `json:"runtime"`
	// Resources caps this session's CPU, memory, and disk. Zero fields mean the runtime's defaults.
	Resources Resources `json:"resources"`
	SandboxID string    `json:"sandbox_id,omitempty"`
	// OnWorkspaceVM means this session lives on the shared machine. Persisted, so a stopped session
	// still knows where it belongs.
	OnWorkspaceVM bool `json:"on_workspace_vm,omitempty"`
	// UseLLMProxy sends this session's LLM requests to the node's LLM proxy. False uses the agent's
	// own login. Does nothing when the node has no proxy configured.
	UseLLMProxy bool `json:"use_llm_proxy"`
	// WorkDir is where the agent and the shell start, a folder under the workspace root. Empty falls
	// back to the first mount, then /workspace.
	WorkDir string `json:"work_dir,omitempty"`
	// AgentWorkDir is the folder the agent works in, answered by the runtime and filled on the way out,
	// never stored. A runtime that makes the folder itself is the only one that can name it.
	AgentWorkDir   string    `json:"agent_work_dir,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastActivityAt time.Time `json:"last_activity_at"`

	// Host is the node this session lives on. Set only by ListAggregated (fan-out); never persisted.
	// nil on the single-session endpoints.
	Host *NodeRef `json:"host,omitempty"`
}

// Empty Image / SetupScript / Name fall back to agent registry defaults.
type CreateRequest struct {
	Agent       string            `json:"agent"`
	Name        string            `json:"name,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Mounts      []MountRequest    `json:"mounts,omitempty"`
	Image       string            `json:"image,omitempty"`
	SetupScript string            `json:"setup_script,omitempty"`
	// Runtime picks which sandbox runtime hosts the session. Empty means the node default.
	Runtime string `json:"runtime,omitempty"`
	// Resources caps the session. Omitted or zero fields fall back to the runtime's defaults.
	Resources Resources `json:"resources,omitempty"`
	// NodeID targets which node hosts the session (a node's StableNodeID). Empty, or this node's id,
	// means create here; another node's id proxies the create to that node over the tailnet. The
	// session lives on the node that creates it.
	NodeID string `json:"node_id,omitempty"`
	// OnWorkspaceVM puts the session on this node's shared machine instead of building it one. The
	// machine owns the mounts, so a session on it takes none.
	OnWorkspaceVM bool `json:"on_workspace_vm,omitempty"`
	// UseLLMProxy sends this session to the node's LLM proxy. Omitted means no.
	UseLLMProxy *bool `json:"use_llm_proxy,omitempty"`
	// WorkDir is the folder this session works in, created if missing. A bare name is taken as a folder
	// under the workspace root; empty means one named after the session. Workspace sessions only.
	WorkDir string `json:"work_dir,omitempty"`
}

// MountRequest is one entry in CreateRequest.Mounts: a host path mapped into the sandbox at a
// guest path. Wire format keeps snake_case field names for JSON-by-convention.
//
// Mounts are optional. The conventional target is /workspace (your
// code), which the agent cd's into; mount nothing and the agent gets
// an empty /workspace in the guest instead. /root is reserved (the
// agent's home); duplicate guest paths are rejected.
type MountRequest struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	// Mode is "rw" (default) or "ro". Empty == "rw". Anything
	// else fails validation.
	Mode string `json:"mode,omitempty"`
}

// ListFilter narrows GET /api/sessions. Today only the "all" toggle matters: by default we hide
// stopped/errored sessions, so `session ps` lists only running ones unless -a.
type ListFilter struct {
	IncludeAll bool
}

// Resources is the per session cap on CPU, memory, and disk.
//
// Units are human facing (whole cores, MiB) rather than the byte counts the sandbox layer uses, because
// these come straight from a form field. Zero means "no explicit limit", which each runtime resolves to
// its own default.
type Resources struct {
	// Size names one of the VM sizes, Small, Medium or Large. When set, CPU and memory come from the size
	// each time the VM boots, so the numbers below stay empty.
	Size string `json:"size,omitempty"`
	// CPU is the number of cores. A VM rounds to whole vCPUs, since you cannot give a virtual machine
	// half a processor.
	CPU float64 `json:"cpu,omitempty"`
	// MemoryMB is the memory cap. For a VM this is the RAM the guest is given, not a ceiling it may
	// exceed, so it is worth setting deliberately.
	MemoryMB int64 `json:"memory_mb,omitempty"`
	// DiskMB caps the session's writable layer.
	//
	// For a VM it sizes the writable overlay, which today is a tmpfs and therefore comes OUT OF the
	// guest's memory: disk and memory share one budget, and setting disk close to memory will starve
	// the agent. It still earns its place, because an uncapped overlay lets a runaway write consume all
	// of the guest's RAM.
	DiskMB int64 `json:"disk_mb,omitempty"`
}

// Bounds on a request. These are sanity rails rather than policy: they exist so a mistyped value fails
// here, as a 400 naming the field, instead of deep inside a hypervisor that was asked for ten terabytes
// of RAM. A node that genuinely cannot honour a plausible request still fails at Create.
const (
	maxCPUCores = 64
	maxMB       = 512 << 10 // 512 GiB
)

// Validate rejects a request that cannot be honoured. Zero always passes: it means "no explicit limit",
// which is what every session had before these caps existed.
func (r Resources) Validate() error {
	if r.CPU < 0 || r.CPU > maxCPUCores {
		return fmt.Errorf("cpu must be between 0 and %d, got %v", maxCPUCores, r.CPU)
	}
	if r.MemoryMB < 0 || r.MemoryMB > maxMB {
		return fmt.Errorf("memory_mb must be between 0 and %d, got %d", maxMB, r.MemoryMB)
	}
	if r.DiskMB < 0 || r.DiskMB > maxMB {
		return fmt.Errorf("disk_mb must be between 0 and %d, got %d", maxMB, r.DiskMB)
	}
	return nil
}

// Asked returns whether this request caps anything at all. Zero means the runtime decides.
func (r Resources) Asked() bool {
	return r.Size != "" || r.CPU != 0 || r.MemoryMB != 0 || r.DiskMB != 0
}
