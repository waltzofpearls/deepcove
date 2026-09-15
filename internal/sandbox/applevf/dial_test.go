package applevf

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

// stuckConnect never answers, the way Virtualization.framework behaves when nothing listens on the port.
// Its goroutines are released at the end of the test so they do not follow us into the next one.
func stuckConnect(t *testing.T) connectFunc {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(uint32) (net.Conn, error) {
		<-release
		return nil, errors.New("released")
	}
}

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// The bug this exists for: a guest that stopped listening used to cost two goroutines per dial, for as
// long as the process lived, with nothing to stop the pile growing.
func TestDialBoundsAGuestThatNeverAnswers(t *testing.T) {
	d := newDialer(stuckConnect(t), nil)

	before := runtime.NumGoroutine()
	for range maxPendingDials * 4 {
		if _, err := d.Dial(shortCtx(t), 1024); err == nil {
			t.Fatal("a guest that never answers should not produce a connection")
		}
	}

	if got := d.Pending(); got != maxPendingDials {
		t.Errorf("pending = %d, want %d", got, maxPendingDials)
	}
	// Goroutines are the whole point: the count must stop growing, not merely grow slower.
	if grew := runtime.NumGoroutine() - before; grew > maxPendingDials {
		t.Errorf("%d goroutines were left behind, want at most %d", grew, maxPendingDials)
	}
}

// Once the cap is reached a dial has to fail immediately, rather than waiting out its own timeout.
func TestDialRefusesWithoutWaiting(t *testing.T) {
	d := newDialer(stuckConnect(t), nil)
	for range maxPendingDials {
		_, _ = d.Dial(shortCtx(t), 1024)
	}

	start := time.Now()
	_, err := d.Dial(context.Background(), 1024)
	if err == nil {
		t.Fatal("the dial should have been refused")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
		t.Errorf("a refused dial took %s, so it waited on something", elapsed)
	}
}

// A dial that lands after the caller gave up must be closed, not dropped on the floor.
func TestDialClosesALateConnection(t *testing.T) {
	late := make(chan net.Conn, 1)
	closed := make(chan struct{})
	d := newDialer(func(uint32) (net.Conn, error) {
		conn := <-late
		return conn, nil
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
		ours, theirs := net.Pipe()
		go func() {
			// The far end sees the close as an EOF on its read.
			buf := make([]byte, 1)
			_, _ = theirs.Read(buf)
			close(closed)
		}()
		late <- ours
	}()

	if _, err := d.Dial(ctx, 1024); err == nil {
		t.Fatal("the dial was abandoned, so it should not return a connection")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Error("the late connection was never closed")
	}
}

// A guest that answers must leave nothing behind.
func TestDialReleasesWhenItAnswers(t *testing.T) {
	d := newDialer(func(uint32) (net.Conn, error) {
		ours, _ := net.Pipe()
		return ours, nil
	}, nil)

	conn, err := d.Dial(context.Background(), 1024)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()

	if got := d.Pending(); got != 0 {
		t.Errorf("pending = %d after a dial that answered, want 0", got)
	}
}

// A guest that refuses is answering: the dial returns and the count comes back down.
func TestDialReleasesOnError(t *testing.T) {
	d := newDialer(func(uint32) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}, nil)

	if _, err := d.Dial(context.Background(), 1024); err == nil {
		t.Fatal("a refused connection should be an error")
	}
	if got := d.Pending(); got != 0 {
		t.Errorf("pending = %d after a refusal, want 0", got)
	}
}

// The warning is what tells us this is happening without a stack dump, and it fires once per machine.
func TestDialWarnsOnce(t *testing.T) {
	var mu sync.Mutex
	var warnings int
	logger := testLogger(func() {
		mu.Lock()
		defer mu.Unlock()
		warnings++
	})

	d := newDialer(stuckConnect(t), logger)
	for range maxPendingDials * 3 {
		_, _ = d.Dial(shortCtx(t), 1024)
	}

	mu.Lock()
	defer mu.Unlock()
	if warnings != 1 {
		t.Errorf("logged %d warnings, want exactly 1", warnings)
	}
}
