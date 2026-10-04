package vm

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestSilence_Record(t *testing.T) {
	type silenceCheck struct {
		at       time.Duration
		answered bool
	}
	cases := []struct {
		name   string
		checks []silenceCheck
		want   bool
	}{
		{"answering", []silenceCheck{{0, true}, {20 * time.Minute, true}}, false},
		{"silent under the limit", []silenceCheck{{0, false}, {9 * time.Minute, false}}, false},
		{"silent for the limit", []silenceCheck{{0, false}, {10 * time.Minute, false}}, true},
		{"an answer starts over", []silenceCheck{{0, false}, {9 * time.Minute, true}, {10 * time.Minute, false}, {19 * time.Minute, false}}, false},
	}
	start := time.Unix(1000, 0)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var quiet silence
			var got bool
			for _, check := range tc.checks {
				got = quiet.record(check.answered, start.Add(check.at), 10*time.Minute)
			}
			if got != tc.want {
				t.Errorf("record = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDriver_WatchForSilence(t *testing.T) {
	cases := []struct {
		name          string
		stoppedBefore bool
		wantStopped   bool
	}{
		{"a silent guest is stopped", false, true},
		{"a guest already stopped is left alone", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var told []sandbox.ID
			driver := &Driver{
				logs:                  t.TempDir(),
				logger:                slog.New(slog.DiscardHandler),
				sandboxes:             map[sandbox.ID]*sandboxState{},
				silentGuestCheckEvery: time.Millisecond,
				silentGuestLimit:      10 * time.Millisecond,
				onSilentGuestStopped:  func(sandboxID sandbox.ID) { told = append(told, sandboxID) },
			}
			// Never answers the control port.
			machine := newFakeMachine()
			guest := NewGuest(machine, nil)
			driver.sandboxes["vm-one"] = &sandboxState{machine: guest}
			if tc.stoppedBefore {
				driver.sandboxes["vm-one"].machine = nil
			}

			driver.watchForSilence("vm-one", guest)

			if got := len(told) == 1 && told[0] == "vm-one"; got != tc.wantStopped {
				t.Errorf("told = %v, want stopped %v", told, tc.wantStopped)
			}
			if machine.Running() == tc.wantStopped {
				t.Errorf("machine running = %v, want %v", machine.Running(), !tc.wantStopped)
			}
			note, _ := os.ReadFile(filepath.Join(driver.logs, "vm-one.log"))
			if got := strings.Contains(string(note), "stopped answering"); got != tc.wantStopped {
				t.Errorf("console note written = %v, want %v", got, tc.wantStopped)
			}
		})
	}
}

func TestDriver_StopSilentGuest_StopWaitsForTheKill(t *testing.T) {
	driver := &Driver{
		logs:      t.TempDir(),
		logger:    slog.New(slog.DiscardHandler),
		sandboxes: map[sandbox.ID]*sandboxState{},
	}
	machine := newFakeMachine()
	machine.stopDelay = 200 * time.Millisecond
	guest := NewGuest(machine, nil)
	driver.sandboxes["vm-one"] = &sandboxState{machine: guest}

	go driver.stopSilentGuest("vm-one", guest, silence{})
	// The kill is under way when the user clicks Stop. Its polite shutdown fails, since the guest is silent.
	time.Sleep(50 * time.Millisecond)
	_ = driver.Stop(context.Background(), "vm-one")
	if machine.Running() {
		t.Error("Stop returned while the old VM was still running, so a Start would boot a second one")
	}
}
