//go:build integration

package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// TestExec_TTY_Roundtrip is a low-level diagnostic test: it bypasses
// the sandbox.Sandbox abstraction and drives moby/client directly to
// confirm the hijacked TTY stream round-trips bytes. If this test
// passes but our bridge doesn't, the bridge is broken; if this test
// also fails, the moby client (or our use of it) is the problem.
//
// Each case feeds bytes into `cat`, reads the kernel-echoed line +
// cat's own write back, and looks for the input substring. The same
// container is reused across cases to amortise startup.
func TestExec_TTY_Roundtrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cli.Close()

	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: "alpine:3.20",
			Cmd:   []string{"sleep", "60"},
		},
	})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	tests := []struct {
		name       string
		input      []byte
		wantSubstr string // bytes we expect to see in the response (kernel echo + cat write)
	}{
		{name: "ascii line", input: []byte("hello-cat\n"), wantSubstr: "hello-cat"},
		{name: "another line", input: []byte("second-line\n"), wantSubstr: "second-line"},
		{name: "unicode bytes", input: []byte("café-ünïcödé\n"), wantSubstr: "café-ünïcödé"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execResp, err := cli.ExecCreate(ctx, created.ID, client.ExecCreateOptions{
				Cmd:          []string{"cat"},
				TTY:          true,
				AttachStdin:  true,
				AttachStdout: true,
				AttachStderr: true,
			})
			if err != nil {
				t.Fatalf("ExecCreate: %v", err)
			}

			attach, err := cli.ExecAttach(ctx, execResp.ID, client.ExecAttachOptions{TTY: true})
			if err != nil {
				t.Fatalf("ExecAttach: %v", err)
			}
			defer attach.Close()

			if _, err := attach.Conn.Write(tc.input); err != nil {
				t.Fatalf("Conn.Write: %v", err)
			}

			deadline := time.Now().Add(2 * time.Second)
			if err := attach.Conn.SetReadDeadline(deadline); err != nil {
				t.Fatalf("SetReadDeadline: %v", err)
			}
			buf := make([]byte, 1024)
			var collected strings.Builder
			for {
				n, rerr := attach.Reader.Read(buf)
				if n > 0 {
					collected.Write(buf[:n])
				}
				if rerr != nil {
					break
				}
				if time.Now().After(deadline) {
					break
				}
			}
			got := collected.String()
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("expected %q in output; got %q", tc.wantSubstr, got)
			}
		})
	}
}
