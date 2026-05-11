//go:build integration

// Integration test for the Docker driver. Requires a running Docker
// daemon reachable via DOCKER_HOST or the platform default socket.
// Run with:
//
//	go test -tags=integration ./internal/sandbox/docker/...
package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestDriver_Lifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	d, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	h, err := d.Create(ctx, sandbox.SandboxSpec{
		Image: "alpine:3.20",
		Cmd:   []string{"sleep", "60"},
		Env:   map[string]string{"GREETING": "hello-from-env"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("created: %s", string(h)[:12])
	t.Cleanup(func() { _ = d.Destroy(context.Background(), h) })

	if err := d.Start(ctx, h); err != nil {
		t.Fatalf("Start: %v", err)
	}

	proc, err := d.Exec(ctx, h, sandbox.ExecSpec{
		Cmd: []string{"sh", "-c", "echo hello-from-exec; echo GREETING=$GREETING"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatalf("Stdout read: %v", err)
	}
	exitCode, err := proc.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code: want 0, got %d", exitCode)
	}
	got := string(out)
	for _, want := range []string{"hello-from-exec", "GREETING=hello-from-env"} {
		if !strings.Contains(got, want) {
			t.Errorf("Stdout missing %q; got %q", want, got)
		}
	}

	if err := d.Stop(ctx, h); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := d.Destroy(ctx, h); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

// TestProcess_Close exercises the leak fix: Close must tear down the
// pump goroutine even when the caller never reads Stdout/Stderr.
func TestProcess_Close(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	d, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	h, err := d.Create(ctx, sandbox.SandboxSpec{
		Image: "alpine:3.20",
		Cmd:   []string{"sleep", "60"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = d.Destroy(context.Background(), h) })

	if err := d.Start(ctx, h); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Run a long-output command, never read it, just Close. If the
	// pump goroutine fix is broken, Close blocks indefinitely.
	proc, err := d.Exec(ctx, h, sandbox.ExecSpec{
		Cmd: []string{"yes", "spam"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- proc.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s — pump goroutine likely leaked")
	}
}

// TestDriver_StubsReturnErrNotSupported confirms Logs/CopyIn/CopyOut/
// Snapshot/Restore all return the sentinel so callers can detect
// portably with errors.Is.
func TestDriver_StubsReturnErrNotSupported(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	if _, err := d.Snapshot(ctx, "irrelevant"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Snapshot: want ErrNotSupported, got %v", err)
	}
	if err := d.Restore(ctx, "irrelevant", ""); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Restore: want ErrNotSupported, got %v", err)
	}
	if _, err := d.Logs(ctx, "irrelevant", sandbox.LogOpts{}); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Logs: want ErrNotSupported, got %v", err)
	}
	if err := d.CopyIn(ctx, "irrelevant", "/a", "/b"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("CopyIn: want ErrNotSupported, got %v", err)
	}
	if err := d.CopyOut(ctx, "irrelevant", "/a", "/b"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("CopyOut: want ErrNotSupported, got %v", err)
	}
}

// TestDriver_NotFoundError confirms operations on a non-existent
// handle return sandbox.ErrNotFound via errors.Is.
func TestDriver_NotFoundError(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	err = d.Start(ctx, "definitely-not-a-real-container-id-xyz")
	if !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Start on missing handle: want ErrNotFound, got %v", err)
	}
}
