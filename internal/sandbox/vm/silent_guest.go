package vm

import (
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A guest that stops answering does not come back: on Apple VF a frozen vsock stays frozen. So the
// driver stops one that has been silent too long, and its session can be started again.
const (
	silentGuestCheckEvery = 30 * time.Second
	silentGuestLimit      = 10 * time.Minute
)

// silence is when a guest stopped answering, and how many checks it has missed since.
type silence struct {
	since  time.Time
	missed int
}

// record notes one check and reports whether the guest has now been silent for limit or longer.
func (s *silence) record(answered bool, now time.Time, limit time.Duration) bool {
	if answered {
		*s = silence{}
		return false
	}
	if s.missed == 0 {
		s.since = now
	}
	s.missed++
	return now.Sub(s.since) >= limit
}

// watchForSilence checks the guest until something else stops it, or stops it itself once it has been
// silent too long.
func (d *Driver) watchForSilence(sandboxID sandbox.ID, guest *Guest) {
	ticker := time.NewTicker(d.silentGuestCheckEvery)
	defer ticker.Stop()
	var quiet silence
	for range ticker.C {
		if !d.isCurrentMachine(sandboxID, guest) {
			return
		}
		if quiet.record(guest.Ping() == nil, time.Now(), d.silentGuestLimit) {
			d.stopSilentGuest(sandboxID, guest, quiet)
			return
		}
	}
}

func (d *Driver) isCurrentMachine(sandboxID sandbox.ID, guest *Guest) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.sandboxes[sandboxID]
	return state != nil && state.machine == guest
}

// stopSilentGuest stops the VM without asking the guest, which would not answer, and leaves a note at
// the end of its console log.
func (d *Driver) stopSilentGuest(sandboxID sandbox.ID, guest *Guest, quiet silence) {
	// Killed before it is let go, so a Stop or Restart meanwhile waits for the kill instead of booting a
	// second VM on the same home disk.
	err := guest.ForceStop()
	d.mu.Lock()
	state := d.sandboxes[sandboxID]
	ours := state != nil && state.machine == guest
	if ours {
		state.machine = nil
	}
	d.mu.Unlock()
	if !ours {
		return
	}
	// Written after the stop, so the note is the last line and not mixed into the guest's output.
	if file := openConsoleLog(d.consolePath(sandboxID)); file != nil {
		_, _ = fmt.Fprintf(file, "deepcove: the guest stopped answering at %s, missed %d checks, stopped at %s\n",
			quiet.since.Format(time.RFC3339), quiet.missed, time.Now().Format(time.RFC3339))
		_ = file.Close()
	}
	d.logger.Warn("stopped a vm that stopped answering", "handle", sandboxID,
		"silent_since", quiet.since, "missed_checks", quiet.missed, "err", err)
	if d.onSilentGuestStopped != nil {
		d.onSilentGuestStopped(sandboxID)
	}
}
