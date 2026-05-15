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

// TestDriver_Lifecycle exercises Create → Start → Exec → Stop →
// Destroy end-to-end. Cases vary the agent-side command and the
// environment so the test surfaces issues like env propagation
// regressions alongside basic lifecycle.
func TestDriver_Lifecycle(t *testing.T) {
	tests := []struct {
		name             string
		spec             sandbox.SandboxSpec
		execCmd          []string
		wantStdoutContains []string
		wantExitCode     int
	}{
		{
			name: "echo with env propagation",
			spec: sandbox.SandboxSpec{
				Image: "alpine:3.20",
				Cmd:   []string{"sleep", "60"},
				Env:   map[string]string{"GREETING": "hello-from-env"},
			},
			execCmd:            []string{"sh", "-c", "echo hello-from-exec; echo GREETING=$GREETING"},
			wantStdoutContains: []string{"hello-from-exec", "GREETING=hello-from-env"},
			wantExitCode:       0,
		},
		{
			name: "exec returns non-zero exit code",
			spec: sandbox.SandboxSpec{
				Image: "alpine:3.20",
				Cmd:   []string{"sleep", "60"},
			},
			execCmd:      []string{"sh", "-c", "exit 7"},
			wantExitCode: 7,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			d, err := New()
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer d.Close()

			h, err := d.Create(ctx, tc.spec)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			t.Logf("created: %s", string(h)[:12])
			t.Cleanup(func() { _ = d.Destroy(context.Background(), h) })

			if err := d.Start(ctx, h); err != nil {
				t.Fatalf("Start: %v", err)
			}

			proc, err := d.Exec(ctx, h, sandbox.ExecSpec{Cmd: tc.execCmd})
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
			if exitCode != tc.wantExitCode {
				t.Errorf("exit code: want %d, got %d", tc.wantExitCode, exitCode)
			}
			got := string(out)
			for _, want := range tc.wantStdoutContains {
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
		})
	}
}

// TestProcess_Close is the regression test for the pump-goroutine
// leak — Close must tear down the connection even when callers
// never drain Stdout/Stderr. Cases cover the leak shape (long-
// output command) and the trivial shape (already-exited process).
func TestProcess_Close(t *testing.T) {
	tests := []struct {
		name    string
		execCmd []string
	}{
		{
			name:    "long-output command never drained",
			execCmd: []string{"yes", "spam"},
		},
		{
			name:    "short-exit command, Close after Wait",
			execCmd: []string{"sh", "-c", "echo done"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

			proc, err := d.Exec(ctx, h, sandbox.ExecSpec{Cmd: tc.execCmd})
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
		})
	}
}

// TestDriver_NotSupported confirms every stubbed Sandbox method
// returns ErrNotSupported so callers can detect them portably with
// errors.Is. Add a row here when implementing a new stub or when
// promoting a stub to a real implementation.
func TestDriver_NotSupported(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	tests := []struct {
		name string
		call func(d *Driver) error
	}{
		{
			name: "Snapshot",
			call: func(d *Driver) error {
				_, err := d.Snapshot(context.Background(), "irrelevant")
				return err
			},
		},
		{
			name: "Restore",
			call: func(d *Driver) error {
				return d.Restore(context.Background(), "irrelevant", "")
			},
		},
		{
			name: "Logs",
			call: func(d *Driver) error {
				_, err := d.Logs(context.Background(), "irrelevant", sandbox.LogOpts{})
				return err
			},
		},
		{
			name: "CopyIn",
			call: func(d *Driver) error {
				return d.CopyIn(context.Background(), "irrelevant", "/a", "/b")
			},
		},
		{
			name: "CopyOut",
			call: func(d *Driver) error {
				return d.CopyOut(context.Background(), "irrelevant", "/a", "/b")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(d); !errors.Is(err, sandbox.ErrNotSupported) {
				t.Errorf("want ErrNotSupported, got %v", err)
			}
		})
	}
}

// TestDriver_NotFound confirms lifecycle operations on a non-
// existent handle normalize to sandbox.ErrNotFound via errors.Is.
// Add a row when adding new lifecycle methods to keep the contract
// honest.
func TestDriver_NotFound(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	const missingHandle = sandbox.Handle("definitely-not-a-real-container-id-xyz")

	tests := []struct {
		name string
		call func(d *Driver) error
	}{
		{
			name: "Start on missing handle",
			call: func(d *Driver) error {
				return d.Start(context.Background(), missingHandle)
			},
		},
		{
			name: "Stop on missing handle",
			call: func(d *Driver) error {
				return d.Stop(context.Background(), missingHandle)
			},
		},
		{
			name: "Destroy on missing handle",
			call: func(d *Driver) error {
				return d.Destroy(context.Background(), missingHandle)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(d); !errors.Is(err, sandbox.ErrNotFound) {
				t.Errorf("want ErrNotFound, got %v", err)
			}
		})
	}
}
