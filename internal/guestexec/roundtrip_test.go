//go:build unix

package guestexec

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startPair wires a Client to a Serve loop over an in memory connection, exercising the real protocol on
// both sides without needing a VM.
func startPair(t *testing.T, req ExecRequest) *Client {
	t.Helper()
	hostConn, guestConn := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(guestConn) }()

	client, err := Start(hostConn, req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the client closed")
		}
	})
	return client
}

func TestExecCapturesStdoutAndExitCode(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "echo hello from the guest"}})

	out, err := io.ReadAll(client.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello from the guest" {
		t.Errorf("stdout = %q", got)
	}

	code, err := client.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestExecSeparatesStderrWithoutTTY(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "echo to-out; echo to-err >&2"}})

	errOut := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(client.Stderr())
		errOut <- strings.TrimSpace(string(b))
	}()
	out, _ := io.ReadAll(client.Stdout())

	if got := strings.TrimSpace(string(out)); got != "to-out" {
		t.Errorf("stdout = %q, want to-out", got)
	}
	select {
	case got := <-errOut:
		if got != "to-err" {
			t.Errorf("stderr = %q, want to-err", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("timed out reading stderr")
	}
	if _, err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestExecPropagatesNonZeroExit(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "exit 42"}})
	code, err := client.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestExecStdinReachesTheProcess(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/cat"}})

	if _, err := client.Stdin().Write([]byte("round trip\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	// Closing stdin gives cat its EOF; without it the command would never finish.
	if err := client.Stdin().Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}

	out, err := io.ReadAll(client.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "round trip" {
		t.Errorf("stdout = %q, want %q", got, "round trip")
	}
	if _, err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestExecPassesEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	client := startPair(t, ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo $DEEPCOVE_TEST; pwd"},
		Env:  map[string]string{"DEEPCOVE_TEST": "from-env"},
		Dir:  dir,
	})

	out, _ := io.ReadAll(client.Stdout())
	got := string(out)
	if !strings.Contains(got, "from-env") {
		t.Errorf("env not passed through; output = %q", got)
	}
	// macOS reports /var as /private/var, so match the suffix rather than the full path.
	if !strings.Contains(got, filepathBase(dir)) {
		t.Errorf("working dir not applied; output = %q, want it to contain %q", got, dir)
	}
	if _, err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// A TTY merges stderr into stdout the way a real terminal does, which is what the terminal attach path
// depends on.
func TestExecTTYMergesStderrIntoStdout(t *testing.T) {
	client := startPair(t, ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo to-err >&2"},
		Tty:  true, Cols: 80, Rows: 24,
	})

	out, _ := io.ReadAll(client.Stdout())
	if !strings.Contains(string(out), "to-err") {
		t.Errorf("stderr should surface on stdout under a TTY; got %q", out)
	}
	if _, err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// The terminal reports the size it was given, which proves the request dimensions reach the pty.
func TestExecTTYAppliesRequestedSize(t *testing.T) {
	client := startPair(t, ExecRequest{
		Argv: []string{"/bin/sh", "-c", "stty size"},
		Tty:  true, Cols: 120, Rows: 40,
	})

	out, _ := io.ReadAll(client.Stdout())
	if got := strings.TrimSpace(string(out)); !strings.Contains(got, "40 120") {
		t.Errorf("stty size = %q, want rows 40 cols 120", got)
	}
	if _, err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestExecSignalTerminatesTheProcess(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "sleep 30"}})

	// Give the shell a moment to be running before signalling it.
	time.Sleep(200 * time.Millisecond)
	if err := client.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := client.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait after signal: %v", err)
	}
	if code == 0 {
		t.Error("a killed process should not report success")
	}
}

func TestExecResizeIsAcceptedMidStream(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/cat"}, Tty: true, Cols: 80, Rows: 24})
	if err := client.Resize(100, 30); err != nil {
		t.Errorf("Resize: %v", err)
	}
	_ = client.Close()
}

func TestWaitRespectsContextCancellation(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "sleep 30"}})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := client.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait error = %v, want context deadline exceeded", err)
	}
}

// A guest that dies without reporting status must surface an error, not a silent exit 0.
func TestWaitReportsConnectionLoss(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	// net.Pipe is unbuffered, so the request write only completes once someone reads it. Consume the
	// request, then vanish without ever sending an exit frame.
	go func() {
		_, _ = ReadRequest(guestConn)
		_ = guestConn.Close()
	}()

	client, err := Start(hostConn, ExecRequest{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	code, waitErr := client.Wait(context.Background())
	if waitErr == nil {
		t.Error("expected an error when the guest vanishes before reporting exit")
	}
	if code == 0 {
		t.Error("a lost connection must not look like success")
	}
}

// An abandoned exec must not leave a process running inside the VM: nothing would ever read its output,
// and orphans would accumulate for the life of the session.
func TestHostHangupKillsTheCommand(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(guestConn) }()

	client, err := Start(hostConn, ExecRequest{Argv: []string{"/bin/sh", "-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the child actually start
	_ = client.Close()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Error("Serve should return promptly after the host hangs up, not wait out the command")
	}
}

func TestSignalRejectsNonUnixSignal(t *testing.T) {
	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "sleep 5"}})
	if err := client.Signal(fakeSignal{}); err == nil {
		t.Error("expected a non Unix signal to be rejected")
	}
	_ = client.Close()
}

type fakeSignal struct{}

func (fakeSignal) String() string { return "fake" }
func (fakeSignal) Signal()        {}

func filepathBase(p string) string {
	if i := strings.LastIndex(p, string(os.PathSeparator)); i >= 0 {
		return p[i+1:]
	}
	return p
}

// A command run over this channel inherits the server process's environment. That is how the agent gets
// the session's variables now that deepcove starts it here rather than from the entrypoint: init puts
// them on itself, and everything it spawns picks them up.
func TestExecInheritsTheServerEnvironment(t *testing.T) {
	t.Setenv("DEEPCOVE_INHERITED", "from-init")

	client := startPair(t, ExecRequest{Argv: []string{"/bin/sh", "-c", "echo $DEEPCOVE_INHERITED"}})

	out, _ := io.ReadAll(client.Stdout())
	if got := string(out); !strings.Contains(got, "from-init") {
		t.Errorf("output = %q, want the inherited value", got)
	}
	if _, err := client.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// The bug behind the Apple VF jam: a caller that reads only stdout left the pump blocked on stderr, so
// the host stopped reading the guest for good.
func TestClient_KeepsReadingWhenAStreamIsNotRead(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	go func() {
		_, _ = ReadRequest(guestConn)
		_ = WriteFrame(guestConn, FrameStderr, []byte("cat: no such file\n"))
		_ = WriteFrame(guestConn, FrameStdout, []byte("ok"))
		_ = WriteFrame(guestConn, FrameExit, EncodeExit(0))
	}()
	client, err := Start(hostConn, ExecRequest{Argv: []string{"cat"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := readWithin(t, client.Stdout(), 2*time.Second)
	if string(out) != "ok" {
		t.Errorf("stdout = %q, want ok", out)
	}
}

// A reader that stops must not stop the host reading the guest for long: the exec is cut off.
func TestClient_CutsOffOutputPastTheLimit(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	guestErr := make(chan error, 1)
	go func() {
		_, _ = ReadRequest(guestConn)
		for {
			if err := WriteFrame(guestConn, FrameStdout, make([]byte, 64)); err != nil {
				guestErr <- err
				return
			}
		}
	}()
	client, err := startWithLimits(hostConn, ExecRequest{Argv: []string{"yes"}}, 256, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Wait(ctx); !errors.Is(err, errStreamFull) {
		t.Errorf("Wait error = %v, want the cut off", err)
	}
	select {
	case <-guestErr:
	case <-time.After(2 * time.Second):
		t.Error("the guest is still writing into a connection nobody reads")
	}
}

// A terminal on a slow browser falls behind but keeps reading, so it gets all the output.
func TestClient_WaitsForASlowReader(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	go func() {
		_, _ = ReadRequest(guestConn)
		for range 16 {
			_ = WriteFrame(guestConn, FrameStdout, make([]byte, 64))
		}
		_ = WriteFrame(guestConn, FrameExit, EncodeExit(0))
	}()
	client, err := startWithLimits(hostConn, ExecRequest{Argv: []string{"cat"}}, 256, time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	total := 0
	chunk := make([]byte, 64)
	for {
		time.Sleep(5 * time.Millisecond)
		read, err := client.Stdout().Read(chunk)
		total += read
		if err != nil {
			break
		}
	}
	if total != 16*64 {
		t.Errorf("read %d bytes, want %d", total, 16*64)
	}
}

// readWithin reads reader to the end, failing the test instead of hanging when it never ends.
func readWithin(t *testing.T, reader io.Reader, limit time.Duration) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(reader)
		done <- result{data, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read: %v", got.err)
		}
		return got.data
	case <-time.After(limit):
		t.Fatal("the read never finished: the pump stopped reading the guest")
		return nil
	}
}
