//go:build linux

package guestinit

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"

	"golang.org/x/sys/unix"
)

// processTable is the record of this init's live children, and the single owner of waiting on them.
//
// They cannot be separate. An init must reap orphans with wait4(-1), which takes the status of ANY child
// including the ones the exec service started; cmd.Wait then loses the race and returns ECHILD, so an
// exec reports -1 and nobody can tell what the command did. Sessions saw this as tmux calls failing at
// random, which broke SSH, Split, and attach.
//
// So this is the only caller of wait4. Statuses for tracked processes go back to whoever started them,
// and everything else is an orphan and simply discarded.
type processTable struct {
	mu      sync.Mutex
	waiting map[int]chan unix.WaitStatus
}

func newProcessTable() *processTable {
	return &processTable{waiting: map[int]chan unix.WaitStatus{}}
}

// Start launches a process and tracks it. The lock spans both, so the reaper cannot collect a short
// lived child in the window between it starting and being tracked.
func (t *processTable) Start(cmd *exec.Cmd, start func() error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := start(); err != nil {
		return err
	}
	t.waiting[cmd.Process.Pid] = make(chan unix.WaitStatus, 1)
	return nil
}

// Wait returns the exit code the reaper collected. It blocks until the process is reaped, which is what
// cmd.Wait would have done had it been allowed to.
func (t *processTable) Wait(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return -1, fmt.Errorf("guestinit: wait on a process that was never started")
	}
	pid := cmd.Process.Pid

	t.mu.Lock()
	ch, tracked := t.waiting[pid]
	t.mu.Unlock()
	if !tracked {
		return -1, fmt.Errorf("guestinit: pid %d is not tracked", pid)
	}

	ws := <-ch
	t.mu.Lock()
	delete(t.waiting, pid)
	t.mu.Unlock()

	// Let os/exec release its own bookkeeping. It reports ECHILD because the status is already collected,
	// which is expected here and says nothing about how the command finished.
	_ = cmd.Wait()

	switch {
	case ws.Signaled():
		// Match the shell convention, and what exec.ExitError reports for a signalled process.
		return 128 + int(ws.Signal()), nil
	default:
		return ws.ExitStatus(), nil
	}
}

// reapForever is the PID 1 duty: collect every child that exits, so the guest never accumulates zombies,
// and hand the status to a waiter when one is registered. It blocks for the life of the VM.
func (t *processTable) reapForever() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, unix.SIGCHLD)
	for range sigs {
		t.reap()
	}
}

func (t *processTable) reap() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
		if ch, ok := t.waiting[pid]; ok {
			// Deliver but keep the entry. Wait removes it after collecting: deleting here loses the race
			// where the child is reaped before its starter gets round to waiting, and Wait then reports
			// the pid as untracked even though the status is sitting right there.
			ch <- ws
		}
	}
}
