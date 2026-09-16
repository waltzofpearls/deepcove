//go:build !windows

package sandboxless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// installTimeout bounds the download. Generous, because it is one download on a cold machine and never
// runs again once the agent is in the shared agent home.
const installTimeout = 5 * time.Minute

var _ sandbox.AgentProvisioner = (*Driver)(nil)

// Setup reports how far this instance has got with getting the agent ready.
func (d *Driver) Setup(_ context.Context, instanceID sandbox.ID) (sandbox.SetupState, error) {
	inst, err := d.lookup(instanceID)
	if err != nil {
		return sandbox.SetupState{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return inst.setup, nil
}

// AgentLaunchCommand is what the terminal multiplexer runs to start the agent.
func (d *Driver) AgentLaunchCommand(instanceID sandbox.ID) ([]string, error) {
	inst, err := d.lookup(instanceID)
	if err != nil {
		return nil, err
	}
	return []string{"sh", launchScript(inst.dir)}, nil
}

// WorkDir is a folder on the machine, made for this session. Kept outside the instance folder, so
// destroying the sandbox does not take the work with it. Mounts say nothing here: there is no guest to
// mount anything into.
func (d *Driver) WorkDir(sessionID string, _ []sandbox.Mount) string {
	dir, err := d.workspacePath(sessionID)
	if err != nil {
		return ""
	}
	return dir
}

// WorkspaceRoot is where this driver puts the folders it makes.
func (d *Driver) WorkspaceRoot() string {
	return filepath.Join(d.stateDir, "workspaces")
}

// workspacePath is the folder this driver makes for a session. The session id has to be a plain name:
// anything that could climb out of the workspaces folder is refused rather than cleaned up.
func (d *Driver) workspacePath(sessionID string) (string, error) {
	if sessionID == "" || sessionID != filepath.Base(sessionID) || sessionID == "." || sessionID == ".." {
		return "", fmt.Errorf("sandboxless: %q is not a session id", sessionID)
	}
	return filepath.Join(d.WorkspaceRoot(), sessionID), nil
}

// workspaceToRemove returns the one folder RemoveStorage may delete, or an empty path when there is
// nothing to delete. Every check has to pass, and anything unexpected returns an error instead of a
// path, because the alternative is deleting somebody's code.
func (d *Driver) workspaceToRemove(sessionID string) (string, error) {
	dir, err := d.workspacePath(sessionID)
	if err != nil {
		return "", err
	}
	// Symlinks are resolved on both sides before they are compared. A folder that points somewhere else
	// is exactly the case this has to refuse.
	root, err := filepath.EvalSymlinks(d.WorkspaceRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("sandboxless: resolve the workspaces folder: %w", err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("sandboxless: resolve the session folder: %w", err)
	}
	rel, err := filepath.Rel(root, real)
	if err != nil {
		return "", fmt.Errorf("sandboxless: %w", err)
	}
	// One level down, directly inside, and nowhere else. Not the workspaces folder itself, not a parent,
	// not a nested path, not somewhere a link led to.
	if rel != filepath.Base(dir) {
		return "", fmt.Errorf("sandboxless: %s is not this session's folder inside %s", real, root)
	}
	return real, nil
}

// provisionAgent finds the agent or installs it, then writes the script that starts it.
func (d *Driver) provisionAgent(ctx context.Context, instanceID sandbox.ID, inst *instance) {
	agent := inst.spec.Agent
	path, log, err := d.resolveAgent(ctx, agent)
	if err == nil {
		err = writeLaunchScript(inst.dir, path, agent.LaunchCmd)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, live := d.instances[instanceID]; !live {
		return // destroyed while this was installing
	}
	// A stop cleared the state on its way out. Running out of time is a failure like any other, and
	// saying so is what lets a later start try again.
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if err != nil {
		inst.setup = sandbox.SetupState{Status: sandbox.SetupFailed, Log: log + "\n" + err.Error()}
		return
	}
	inst.agentPath = path
	inst.setup = sandbox.SetupState{Status: sandbox.SetupReady, Log: log}
}

// AgentInstalled reports whether this machine already has the agent, on PATH or in the agent home.
func (d *Driver) AgentInstalled(agent sandbox.Agent) bool {
	_, found := d.agentPath(agent)
	return found
}

func (d *Driver) agentPath(agent sandbox.Agent) (string, bool) {
	if agent.Binary == "" {
		return "", false
	}
	if found, ok := d.rememberedAgentPath(agent.Binary); ok {
		return found, true
	}
	if onPath, err := exec.LookPath(agent.Binary); err == nil {
		d.rememberAgentPath(agent.Binary, onPath)
		return onPath, true
	}
	installed := filepath.Join(d.agentHome, ".local", "bin", agent.Binary)
	if isExecutable(installed) {
		d.rememberAgentPath(agent.Binary, installed)
		return installed, true
	}
	return installed, false
}

// rememberedAgentPath saves the walk of every PATH entry that the create form's preflight would
// otherwise pay once per runtime per agent. The path is still proven, so an agent deleted since is
// looked for again.
func (d *Driver) rememberedAgentPath(binary string) (string, bool) {
	d.agentMu.Lock()
	path, known := d.agentPaths[binary]
	d.agentMu.Unlock()
	if !known || !isExecutable(path) {
		return "", false
	}
	return path, true
}

func (d *Driver) rememberAgentPath(binary, path string) {
	d.agentMu.Lock()
	defer d.agentMu.Unlock()
	d.agentPaths[binary] = path
}

// resolveAgent answers where the agent binary is, installing it when this machine has none.
//
// One install at a time, because the agent home is shared by every session on this node: two
// installers writing the same files is how you end up with half an agent. Sessions created together
// queue here, and all but the first find the agent already in place.
func (d *Driver) resolveAgent(ctx context.Context, agent sandbox.Agent) (path, log string, err error) {
	if agent.Binary == "" {
		return "", "", fmt.Errorf("sandboxless: the spec names no agent binary")
	}
	d.installMu.Lock()
	defer d.installMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("sandboxless: waiting for another session to install %s: %w", agent.Binary, err)
	}
	if found, ok := d.agentPath(agent); ok {
		return found, "using the " + agent.Binary + " already here: " + found, nil
	}
	installed, _ := d.agentPath(agent)
	if agent.Install == "" {
		return "", "", fmt.Errorf("sandboxless: no %s here, and this agent has no install command", agent.Binary)
	}

	if err := createAgentHome(d.agentHome); err != nil {
		return "", "", fmt.Errorf("sandboxless: the agent home: %w", err)
	}
	log, err = d.installAgent(ctx, d.agentHome, agent.Install)
	if err != nil {
		return "", log, fmt.Errorf("sandboxless: install %s: %w", agent.Binary, err)
	}
	if !isExecutable(installed) {
		return "", log, fmt.Errorf("sandboxless: the installer left no %s at %s", agent.Binary, installed)
	}
	return installed, log, nil
}

// createAgentHome makes the folder the installer writes into. Made on the way to the first install
// rather than at startup, so a machine that never runs one of these sessions gets no agent files.
func createAgentHome(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return proveWritable(path)
}

// runInstall runs the agent's own install command with HOME pointed at the shared agent home, which is
// where the installers put things. Nothing outside deepcove's data directory is written.
func runInstall(ctx context.Context, agentHome, install string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", install)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	env := allowedEnvVars()
	env["HOME"] = agentHome
	cmd.Env = sortedEnv(env)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// killGroup stops the install and everything it started. The group, not the process: the install is a
// pipeline, so killing the shell alone leaves the download and the installer it feeds running.
//
// A group that has already gone reports that the process is done, which is how os/exec is told that a
// cancel arriving just after the install finished is not a failure.
func killGroup(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func launchScript(instanceDir string) string {
	return filepath.Join(instanceDir, "agent-launch.sh")
}

// The agent's own launch command follows this, and runs $DEEPCOVE_AGENT. Naming the resolved path here
// keeps the agent's script free of paths, and the quoting in Go where it can be tested.
const launchScriptHeader = `#!/bin/sh
export PATH="$HOME/.local/bin:$PATH"
DEEPCOVE_AGENT=%s
export DEEPCOVE_AGENT
`

func writeLaunchScript(instanceDir, agentPath, launchCmd string) error {
	body := fmt.Sprintf(launchScriptHeader, shellQuote(agentPath)) + launchCmd + "\n"
	if err := os.WriteFile(launchScript(instanceDir), []byte(body), 0o700); err != nil {
		return fmt.Errorf("sandboxless: write the launch script: %w", err)
	}
	return nil
}

// agentInLaunchScript reads back the path writeLaunchScript recorded, for a session taken over from a
// previous run. Empty when there is no script, or nothing in it that names an agent.
func agentInLaunchScript(instanceDir string) string {
	body, err := os.ReadFile(launchScript(instanceDir))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		quoted, found := strings.CutPrefix(line, "DEEPCOVE_AGENT=")
		if !found {
			continue
		}
		return shellUnquote(quoted)
	}
	return ""
}

// shellUnquote reverses shellQuote.
func shellUnquote(s string) string {
	return strings.ReplaceAll(strings.Trim(s, "'"), `'\''`, "'")
}

// shellQuote wraps a path for sh. Home directories carry apostrophes more often than you would think.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
