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

// SweepOrphans clears out what a previous deepcove left: the tmux servers still running, their
// sockets, and the folders their sessions worked from.
//
// A tmux server outlives the deepcove that started it, and the instances are only in memory, so after a
// restart nothing here knows about them and nothing would ever stop them.
func (d *Driver) SweepOrphans(ctx context.Context) (int, error) {
	swept, stillRunning, err := d.sweepSockets(ctx)
	if stillRunning == nil {
		// The socket folder could not be read, so nothing here knows which servers are still up. Their
		// folders are the only way back to them, so none of them go.
		return swept, err
	}
	// Before the folders go: a session we adopt keeps its own.
	err = errors.Join(err, d.adoptSurvivors(stillRunning))
	// Counted separately: a folder left behind is not a session that had to be stopped.
	return swept, errors.Join(err, d.sweepInstanceDirs())
}

// swept is how many servers this stopped, and stillRunning holds the short ids of the ones it could not.
func (d *Driver) sweepSockets(ctx context.Context) (swept int, stillRunning map[string]bool, err error) {
	entries, readErr := os.ReadDir(d.socketDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			// Nothing to sweep, and nothing unaccounted for either.
			return 0, map[string]bool{}, nil
		}
		return 0, nil, fmt.Errorf("sandboxless: read %s: %w", d.socketDir, readErr)
	}

	stillRunning = map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sock") {
			continue
		}
		socket := filepath.Join(d.socketDir, entry.Name())
		if d.socketHasAnInstance(socket) {
			continue
		}
		if killErr := d.killServer(ctx, socket); killErr != nil {
			err = errors.Join(err, killErr)
			stillRunning[strings.TrimSuffix(entry.Name(), ".sock")] = true
			continue
		}
		err = errors.Join(err, os.Remove(socket))
		swept++
	}
	return swept, stillRunning, err
}

// adoptSurvivors takes back the sessions whose servers would not stop, so the session layer finds them
// where it left them. Without this their rows read as lost, and rebuilding one puts a second agent in
// the folder the first is still working in.
func (d *Driver) adoptSurvivors(stillRunning map[string]bool) error {
	if len(stillRunning) == 0 {
		return nil
	}
	entries, err := os.ReadDir(d.instancesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sandboxless: read %s: %w", d.instancesDir(), err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, entry := range entries {
		instanceID := sandbox.ID(entry.Name())
		if !stillRunning[shortID(instanceID)] {
			continue
		}
		dir := filepath.Join(d.instancesDir(), entry.Name())
		adopted := &instance{
			dir:       dir,
			socket:    filepath.Join(d.socketDir, shortID(instanceID)+".sock"),
			running:   true,
			createdAt: time.Now(),
		}
		// The spec that made it is gone with the previous run, but its launch script names the agent it
		// runs. With that, stopping and starting it again works like any other session.
		if path := agentInLaunchScript(dir); isExecutable(path) {
			adopted.agentPath = path
			adopted.setup = sandbox.SetupState{Status: sandbox.SetupReady}
		}
		d.instances[instanceID] = adopted
	}
	return nil
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
