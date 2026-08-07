//go:build linux

package guestinit

import (
	"os/exec"
	"sync"
	"testing"
	"time"
)

// The bug this exists to prevent: PID 1's reaper collecting a child's status before the code that
// started it can, leaving that code with ECHILD and no idea how the command finished.
func TestProcessTableReturnsStatusDespiteReaping(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want int
	}{
		{"success", []string{"/bin/sh", "-c", "exit 0"}, 0},
		{"non zero exit is the command's own answer", []string{"/bin/sh", "-c", "exit 7"}, 7},
		{"killed by a signal reports 128+signal", []string{"/bin/sh", "-c", "kill -9 $$"}, 137},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := newProcessTable()
			cmd := exec.Command(tc.argv[0], tc.argv[1:]...)
			if err := table.Start(cmd, cmd.Start); err != nil {
				t.Fatalf("Start: %v", err)
			}

			// Reap hard from another goroutine, standing in for reapForever waking on SIGCHLD. Whichever
			// side gets there first, Wait must still return the real status. The loop runs until Wait
			// returns, because in production the signal keeps arriving until the child is collected.
			done := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-done:
						return
					default:
					}
					table.reap()
					time.Sleep(time.Millisecond)
				}
			}()

			code, err := table.Wait(cmd)
			close(done)
			wg.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}

// An orphan is not tracked, so reaping must simply discard it rather than block or panic.
func TestProcessTableDiscardsOrphans(t *testing.T) {
	table := newProcessTable()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil { // started outside the table, like a reparented orphan
		t.Fatalf("Start: %v", err)
	}
	table.reap()
	_ = cmd.Wait()

	if _, err := table.Wait(cmd); err == nil {
		t.Error("Wait on an untracked process should report that it is not tracked")
	}
}

// The child is reaped before anyone waits on it. The status must still be there: deleting the entry on
// delivery made Wait report the pid as untracked.
func TestProcessTableSurvivesReapBeforeWait(t *testing.T) {
	table := newProcessTable()
	cmd := exec.Command("/bin/sh", "-c", "exit 3")
	if err := table.Start(cmd, cmd.Start); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Reap until the child is collected, well before Wait is called.
	for i := 0; i < 500; i++ {
		table.reap()
		table.mu.Lock()
		ch := table.waiting[cmd.Process.Pid]
		delivered := len(ch) > 0
		table.mu.Unlock()
		if delivered {
			break
		}
		time.Sleep(time.Millisecond)
	}

	code, err := table.Wait(cmd)
	if err != nil {
		t.Fatalf("Wait after the reaper already collected it: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}
