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
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// tmuxCallBudget bounds one call to tmux. Adoption runs before deepcove serves anything, and a tmux
// client waiting on a server that has stopped answering waits forever, so one wedged session would
// keep the node from ever starting.
const tmuxCallBudget = 5 * time.Second

// adoptBudget bounds adoption as a whole. Sessions are taken back one after another, so a node with
// several wedged ones would add up to minutes of not starting, even with every call bounded.
const adoptBudget = 30 * time.Second

// Adopt takes back the sessions whose tmux servers are still running, and stops what is left.
func (d *Driver) Adopt(ctx context.Context, sandboxes map[sandbox.ID]sandbox.SandboxSpec) ([]sandbox.ID, error) {
	// Out of time, everything left is kept and nothing is swept, which is the safe way to be wrong.
	ctx, cancel := context.WithTimeout(ctx, d.adoptBudget)
	defer cancel()
	var taken []sandbox.ID
	var err error
	for instanceID, spec := range sandboxes {
		ok, takeErr := d.take(ctx, instanceID, spec)
		err = errors.Join(err, takeErr)
		if ok {
			taken = append(taken, instanceID)
		}
	}
	_, sweepErr := d.sweep(ctx)
	return taken, errors.Join(err, sweepErr)
}

// take rebuilds one instance from its record and the folder its previous run left. The socket and the
// folder are named after the id. The error is what it could not find out, which is not the same as a
// session that is gone.
func (d *Driver) take(ctx context.Context, instanceID sandbox.ID, spec sandbox.SandboxSpec) (bool, error) {
	if !isPlainFolderName(string(instanceID)) {
		return false, nil
	}
	dir := filepath.Join(d.instancesDir(), string(instanceID))
	socket := filepath.Join(d.socketDir, shortID(instanceID)+".sock")

	// Only "it is not there" means gone. A folder or a tmux that cannot be asked about is taken back
	// instead, because a session read as gone is swept and started again, and a second agent in the
	// folder the first is working in is the one outcome worth avoiding.
	info, statErr := os.Stat(dir)
	if os.IsNotExist(statErr) || (statErr == nil && !info.IsDir()) {
		return false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, d.tmuxCallBudget)
	defer cancel()
	alive, probeErr := d.serverAlive(probeCtx, socket)
	if !alive && probeErr == nil {
		return false, nil
	}
	createdAt := time.Now()
	if info != nil {
		createdAt = info.ModTime()
	}
	// Its tmux server answering is the agent running in it, so there is nothing left to install.
	taken := &instance{
		spec:      spec,
		dir:       dir,
		socket:    socket,
		running:   true,
		createdAt: createdAt,
		setup:     sandbox.SetupState{Status: sandbox.SetupReady},
	}
	// What its script will run, not wherever the agent is now. A start has to find this one missing to
	// write the script again, which is what the spec is carried for.
	if path := agentInLaunchScript(dir); isExecutable(path) {
		taken.agentPath = path
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.instances[instanceID] = taken
	return true, errors.Join(statErr, probeErr)
}

// sweep stops every tmux server this driver does not know about and removes what it worked in. A server
// that would not stop keeps both, since its socket is the only way back to it.
func (d *Driver) sweep(ctx context.Context) (swept int, err error) {
	entries, readErr := os.ReadDir(d.socketDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, nil
		}
		// Without the sockets there is no telling what is still up, so no folder goes either.
		return 0, fmt.Errorf("sandboxless: read %s: %w", d.socketDir, readErr)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sock") {
			continue
		}
		socket := filepath.Join(d.socketDir, entry.Name())
		if d.socketHasAnInstance(socket) {
			continue
		}
		killCtx, cancel := context.WithTimeout(ctx, d.tmuxCallBudget)
		killErr := d.killServer(killCtx, socket)
		cancel()
		if killErr != nil {
			err = errors.Join(err, killErr)
			continue
		}
		err = errors.Join(err, os.Remove(socket))
		swept++
	}
	return swept, errors.Join(err, d.sweepInstanceDirs())
}

// sweepInstanceDirs removes the folder of every instance this run does not know about. At startup that
// is all of them, since the registry is memory only, apart from the ones just adopted.
func (d *Driver) sweepInstanceDirs() error {
	entries, err := os.ReadDir(d.instancesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sandboxless: read %s: %w", d.instancesDir(), err)
	}
	var removeErr error
	for _, entry := range entries {
		instanceID := sandbox.ID(entry.Name())
		if d.hasInstance(instanceID) {
			continue
		}
		// Its socket outliving the sweep above means its server would not stop, and it is still working
		// in here.
		if _, err := os.Stat(filepath.Join(d.socketDir, shortID(instanceID)+".sock")); err == nil {
			continue
		}
		removeErr = errors.Join(removeErr, os.RemoveAll(filepath.Join(d.instancesDir(), entry.Name())))
	}
	return removeErr
}

// killTmuxServer stops the server on this socket, and reports an error only when one is still there
// afterwards. A server that was already gone is what the caller wanted.
func killTmuxServer(ctx context.Context, socket string) error {
	if err := exec.CommandContext(ctx, "tmux", "-S", socket, "kill-server").Run(); err == nil {
		return nil
	}
	// A command that died with the context proves nothing about the server.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := exec.CommandContext(ctx, "tmux", "-S", socket, "has-session").Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	return fmt.Errorf("the tmux server on %s is still running", socket)
}

// tmuxServerAlive reports whether a server is answering on this socket. The error is for not being able
// to tell: tmux exiting non-zero is an answer, tmux failing to run at all is not.
func tmuxServerAlive(ctx context.Context, socket string) (bool, error) {
	err := exec.CommandContext(ctx, "tmux", "-S", socket, "has-session").Run()
	if err == nil {
		return true, nil
	}
	// A command that died with the context proves nothing about the server.
	var exited *exec.ExitError
	if errors.As(err, &exited) && ctx.Err() == nil {
		return false, nil
	}
	return false, fmt.Errorf("sandboxless: ask the tmux server on %s: %w", socket, err)
}
