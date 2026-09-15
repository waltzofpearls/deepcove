package vm

import (
	"context"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Shutdown has a budget, and stopping a VM waits on a guest that may never answer. Three of those in a
// row would blow it, so they have to go at once.
func TestCloseStopsMachinesAtOnce(t *testing.T) {
	const stopDelay = 300 * time.Millisecond
	d := &Driver{machines: map[sandbox.ID]*session{}}
	var machines []*fakeMachine
	for _, id := range []sandbox.ID{"one", "two", "three"} {
		m := newFakeMachine()
		m.stopDelay = stopDelay
		m.answerControl()
		machines = append(machines, m)
		d.machines[id] = &session{machine: NewGuest(m, nil)}
	}

	start := time.Now()
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 2*stopDelay {
		t.Errorf("closing three machines took %s, so they were stopped one after another", elapsed)
	}
	for i, m := range machines {
		if m.Running() {
			t.Errorf("machine %d is still running", i)
		}
	}
}

// A wedged shutdown has no time left to ask a guest anything, and asking is what wedged it.
func TestForceCloseDoesNotAskTheGuest(t *testing.T) {
	d := &Driver{machines: map[sandbox.ID]*session{}}
	m := newFakeMachine()
	m.answerControl()
	d.machines["one"] = &session{machine: NewGuest(m, nil)}

	if err := d.ForceClose(); err != nil {
		t.Fatalf("ForceClose: %v", err)
	}
	if m.Running() {
		t.Error("the machine is still running")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dials != 0 {
		t.Errorf("the guest was dialled %d times, want 0", m.dials)
	}
}

// A polite stop does ask, which is the difference the forced one exists to skip.
func TestCloseAsksTheGuest(t *testing.T) {
	d := &Driver{machines: map[sandbox.ID]*session{}}
	m := newFakeMachine()
	m.answerControl()
	d.machines["one"] = &session{machine: NewGuest(m, nil)}

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dials == 0 {
		t.Error("a polite stop should have dialled the guest control service")
	}
}

// Shutdown has a budget. A guest that takes the request and never halts must not spend it all.
func TestCloseForcesAGuestThatWillNotHalt(t *testing.T) {
	d := &Driver{machines: map[sandbox.ID]*session{}}
	m := newFakeMachine()
	m.answerControl()
	m.stubborn = true
	d.machines["one"] = &session{machine: NewGuest(m, nil)}

	start := time.Now()
	err := d.closeWithin(50 * time.Millisecond)
	if err == nil {
		t.Fatal("a forced stop should report that the guests did not go quietly")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("closing took %s, so the budget was not enforced", elapsed)
	}
	if m.Running() {
		t.Error("the machine is still running after the budget ran out")
	}
}

// Both paths tear the same guest down, so doing it twice must be safe and must stop the machine once.
func TestTeardownRunsOnce(t *testing.T) {
	m := newFakeMachine()
	m.answerControl()
	g := NewGuest(m, nil)

	if err := g.ForceStop(); err != nil {
		t.Fatalf("ForceStop: %v", err)
	}
	if err := g.ForceStop(); err != nil {
		t.Fatalf("second ForceStop: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stops != 1 {
		t.Errorf("the machine was stopped %d times, want 1", m.stops)
	}
}

// A VM booted while the node is shutting down used to be invisible to Close, because the driver only
// recorded it once it was ready. On a hypervisor that owns its VM as a separate process, that machine
// outlives deepcove.
func TestCloseStopsAMachineThatIsStillStarting(t *testing.T) {
	d := newTestDriver(t)
	backend := d.backend.(*fakeBackend)

	handle, err := d.Create(context.Background(), sandbox.SandboxSpec{
		Image: "example.com/agent:latest",
		Cmd:   []string{"/bin/sh"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The fake guest never answers the readiness probe, so Start is still booting when Close lands.
	started := make(chan error, 1)
	go func() { started <- d.Start(context.Background(), handle) }()
	booted := waitForMachine(t, backend)

	// Close reports an error here, because a guest that never answered cannot be asked to power off.
	// What matters is that it went down anyway.
	_ = d.Close()
	if booted.Running() {
		t.Error("the machine survived the shutdown")
	}
	if err := <-started; err == nil {
		t.Error("Start reported success for a machine that was stopped under it")
	}
}

// waitForMachine waits for the backend to hand out its first machine.
func waitForMachine(t *testing.T, backend *fakeBackend) *fakeMachine {
	t.Helper()
	for range 200 {
		backend.mu.Lock()
		machines := len(backend.machines)
		backend.mu.Unlock()
		if machines > 0 {
			return backend.machines[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the backend never booted a machine")
	return nil
}

// The contract Machine.Stop carries: a forced stop does not return while the guest is still up. Exiting
// on top of a live VM is what leaves a process nothing can reap.
func TestForceStopLeavesNothingRunning(t *testing.T) {
	m := newFakeMachine()
	m.stopDelay = 50 * time.Millisecond
	g := NewGuest(m, nil)

	if err := g.ForceStop(); err != nil {
		t.Fatalf("ForceStop: %v", err)
	}

	if m.Running() {
		t.Error("the machine is still running after a forced stop returned")
	}
}
