package sandbox

import (
	"context"
	"io"
	"strings"
	"time"
)

// Paths a sandboxed session's entrypoint writes, and its runtime reads back.
const (
	SandboxedSetupStatusPath = "/etc/deepcove-bootstrap-status"
	SandboxedSetupLogPath    = "/var/log/deepcove-bootstrap.log"
	SandboxedAgentLaunchPath = "/usr/local/bin/deepcove-agent-launch"
	SandboxedWorkDir         = "/workspace"
	// SandboxedAgentHome is the agent's home, where guest init mounts the home disk. Agent images run
	// as root today. Nothing may be shared at or under it.
	SandboxedAgentHome = "/root"
)

// Words the entrypoint writes into the status file at SandboxedSetupStatusPath. The guest's own
// vocabulary: it says errored where a SetupStatus says failed.
const (
	SandboxedStatusInstalling = "installing"
	SandboxedStatusReady      = "ready"
	SandboxedStatusErrored    = "errored"
)

// SandboxedProvisioner is the part of AgentProvisioner that is the same for every runtime with a
// guest: the entrypoint installed the agent, wrote the script that starts it and made the folder it
// works in. A driver embeds this and adds Setup, which needs the driver itself to read those files.
type SandboxedProvisioner struct{}

func (SandboxedProvisioner) AgentLaunchCommand(ID) ([]string, error) {
	return []string{SandboxedAgentLaunchPath}, nil
}

func (SandboxedProvisioner) WorkDir(_ string, mounts []Mount) string {
	return SandboxedWorkDirFor(mounts)
}

// Empty: a session's folder here is a mount, not a folder the runtime makes.
func (SandboxedProvisioner) WorkspaceRoot() string {
	return ""
}

// The entrypoint installs the agent inside the sandbox on every boot.
func (SandboxedProvisioner) AgentInstalled(Agent) bool {
	return true
}

// Generous enough for a VM exec, which pays a vsock dial and a process spawn, while still returning
// promptly when a sandbox is wedged.
const sandboxedReadTimeout = 5 * time.Second

// SandboxedSetup reads how far a sandboxed session's own setup has got.
//
// A read that fails reports installing, never ready. The UI gates the terminal attach on this, so a
// ready with no evidence attaches before the agent's tmux session exists and leaves a dead terminal.
func SandboxedSetup(ctx context.Context, sb Sandbox, sandboxID ID) (SetupState, error) {
	state := SetupState{Status: SetupInstalling}
	if raw, ok := readSandboxedFile(ctx, sb, sandboxID, "cat", SandboxedSetupStatusPath); ok {
		switch strings.TrimSpace(string(raw)) {
		case SandboxedStatusReady:
			state.Status = SetupReady
		case SandboxedStatusErrored:
			state.Status = SetupFailed
		}
	}
	// Its own budget: sharing one let a slow log starve the status read, which flickered the spinner.
	if raw, ok := readSandboxedFile(ctx, sb, sandboxID, "tail", "-c", "8192", SandboxedSetupLogPath); ok {
		state.Log = string(raw)
	}
	return state, nil
}

func readSandboxedFile(ctx context.Context, sb Sandbox, sandboxID ID, cmd ...string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(ctx, sandboxedReadTimeout)
	defer cancel()

	proc, err := sb.Exec(ctx, sandboxID, ExecSpec{Cmd: cmd})
	if err != nil {
		return nil, false
	}
	defer func() { _ = proc.Close() }()

	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		return nil, false
	}
	if exit, err := proc.Wait(ctx); err != nil || exit != 0 {
		return nil, false
	}
	return out, true
}

// SandboxedWorkDirFor is where a sandboxed agent works: the first mount's target, else SandboxedWorkDir.
func SandboxedWorkDirFor(mounts []Mount) string {
	if len(mounts) > 0 {
		return mounts[0].Target
	}
	return SandboxedWorkDir
}
