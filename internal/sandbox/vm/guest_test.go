package vm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
)

func TestGuestPing(t *testing.T) {
	m := newFakeMachine()
	g := NewGuest(m, nil)

	if err := g.Ping(); err == nil {
		t.Error("a guest with no control service should not answer")
	}
	m.answerControl()
	if err := g.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// A stopped guest must fail fast rather than wait out the dial timeout.
func TestGuestPingOnAStoppedMachine(t *testing.T) {
	m := newFakeMachine()
	m.answerControl()
	g := NewGuest(m, nil)
	_ = m.Stop()

	if err := g.Ping(); err == nil {
		t.Error("a stopped guest should not answer")
	}
}

func TestGuestStopAsksTheGuestFirst(t *testing.T) {
	m := newFakeMachine()
	m.answerControl()
	g := NewGuest(m, nil)

	if err := g.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.Running() {
		t.Error("the guest is still running after Stop")
	}
	if m.stops != 1 {
		t.Errorf("the backend was stopped %d times, want 1", m.stops)
	}
}

// WaitReady must not report a bare timeout: the console tail is the only evidence of why a guest failed.
func TestWaitReadyReportsTheConsole(t *testing.T) {
	m := newFakeMachine()
	g := NewGuest(m, nil)
	g.CaptureConsole("")
	if _, err := m.console.Write([]byte("Kernel panic - not syncing\n")); err != nil {
		t.Fatal(err)
	}
	// The drain runs in the background, so give it a moment to see the write.
	time.Sleep(20 * time.Millisecond)

	err := g.WaitReady(context.Background(), 10*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady should fail when the guest never answers")
	}
	if !strings.Contains(err.Error(), "Kernel panic") {
		t.Errorf("the error should carry the console tail, got: %v", err)
	}
}

// The guest fetches its configuration over vsock at boot, so the host has to be listening before it asks.
func TestServeConfigAnswersTheGuest(t *testing.T) {
	m := newFakeMachine()
	g := NewGuest(m, nil)
	want := guestcfg.Config{Entrypoint: []string{"/bin/sh"}, Agent: "claude"}
	if err := g.ServeConfig(want); err != nil {
		t.Fatalf("ServeConfig: %v", err)
	}

	conn, err := m.dialHost(guestcfg.Port)
	if err != nil {
		t.Fatalf("dial the config service: %v", err)
	}
	defer func() { _ = conn.Close() }()

	got, err := guestcfg.Fetch(conn)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Agent != want.Agent || len(got.Entrypoint) != 1 || got.Entrypoint[0] != want.Entrypoint[0] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Stopping the guest closes the service listeners: a VM that is gone must not leave a port answering.
func TestStopClosesServices(t *testing.T) {
	m := newFakeMachine()
	m.answerControl()
	g := NewGuest(m, nil)
	if err := g.ServeConfig(guestcfg.Config{}); err != nil {
		t.Fatalf("ServeConfig: %v", err)
	}
	if err := g.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := m.dialHost(guestcfg.Port); err == nil {
		t.Error("the config service still answers after Stop")
	}
}

// countingCloser records how many times it was closed.
type countingCloser struct {
	mu     sync.Mutex
	closed int
}

func (c *countingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

// Services are registered while the VM boots, which can be after a shutdown has already released it.
// Holding onto one then would leak a listener nothing ever closes.
func TestKeepAfterReleaseClosesItStraightAway(t *testing.T) {
	g := NewGuest(newFakeMachine(), nil)
	if err := g.ForceStop(); err != nil {
		t.Fatalf("ForceStop: %v", err)
	}

	closer := &countingCloser{}
	g.keep(closer)

	closer.mu.Lock()
	defer closer.mu.Unlock()
	if closer.closed != 1 {
		t.Errorf("closed %d times, want once", closer.closed)
	}
}

// Run with -race: registering a service and tearing the guest down happen on different goroutines.
func TestKeepWhileReleasing(t *testing.T) {
	g := NewGuest(newFakeMachine(), nil)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.keep(&countingCloser{})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = g.ForceStop()
	}()
	wg.Wait()
}
