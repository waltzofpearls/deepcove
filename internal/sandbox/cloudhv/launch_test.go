package cloudhv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubVMM writes a shell script that stands in for cloud-hypervisor: it creates the API socket the
// launcher waits for, copies its own greeting to the console, and then waits to be signalled.
func stubVMM(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "stub-vmm")
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// runPaths puts every socket in one short directory, the way the backend will.
func runPaths(t *testing.T) paths {
	t.Helper()
	dir := shortTempDir(t)
	return paths{
		api:     filepath.Join(dir, "api.sock"),
		vsock:   filepath.Join(dir, "vsock.sock"),
		console: filepath.Join(dir, "console"),
	}
}

func TestLaunchWaitsForTheAPISocket(t *testing.T) {
	p := runPaths(t)
	// The socket appears late, so the launcher has to wait rather than check once.
	binary := stubVMM(t, `sleep 0.2; : > "$(echo "$@" | sed 's/.*--api-socket path=//;s/ .*//')"; sleep 30`)

	proc, err := launch(binary, testSpec(), p)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = proc.Stop() }()

	if !proc.Running() {
		t.Error("the vmm should be running")
	}
	if _, err := os.Stat(p.api); err != nil {
		t.Errorf("the api socket is missing: %v", err)
	}
}

// A VMM that dies at startup must be reported as that, not as a timeout thirty seconds later.
func TestLaunchReportsAnEarlyExit(t *testing.T) {
	binary := stubVMM(t, `echo "bad configuration" >&2; exit 2`)

	start := time.Now()
	_, err := launch(binary, testSpec(), runPaths(t))
	if err == nil {
		t.Fatal("a vmm that exits should fail the launch")
	}
	if !strings.Contains(err.Error(), "exited before it was ready") {
		t.Errorf("the error should say the vmm exited, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > readyWait {
		t.Errorf("waited %s for a process that was already gone", elapsed)
	}
}

func TestLaunchMissingBinary(t *testing.T) {
	if _, err := launch(filepath.Join(shortTempDir(t), "nope"), testSpec(), runPaths(t)); err == nil {
		t.Error("launching a binary that does not exist should fail")
	}
}

// The console is a pipe: what the VMM writes is what the vm package drains.
func TestConsoleIsAStream(t *testing.T) {
	p := runPaths(t)
	binary := stubVMM(t, `api=$(echo "$@" | sed 's/.*--api-socket path=//;s/ .*//'); `+
		`con=$(echo "$@" | sed 's/.*--console file=//;s/ .*//'); `+
		`echo "hello from the guest" > "$con"; : > "$api"; sleep 30`)

	proc, err := launch(binary, testSpec(), p)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = proc.Stop() }()

	// Read in the background: a read deadline is not available here, because macOS refuses to poll a
	// FIFO opened as a file.
	said := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := proc.Console().Read(buf)
		if err != nil {
			said <- "read failed: " + err.Error()
			return
		}
		said <- string(buf[:n])
	}()
	select {
	case got := <-said:
		if !strings.Contains(got, "hello from the guest") {
			t.Errorf("console said %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("the console said nothing")
	}
}

func TestStopEndsTheProcess(t *testing.T) {
	p := runPaths(t)
	binary := stubVMM(t, `: > "$(echo "$@" | sed 's/.*--api-socket path=//;s/ .*//')"; sleep 30`)

	proc, err := launch(binary, testSpec(), p)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := proc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if proc.Running() {
		t.Error("the vmm is still running after Stop")
	}
	// The sockets it was given must not outlive it.
	if _, err := os.Stat(p.api); !os.IsNotExist(err) {
		t.Errorf("the api socket survived: %v", err)
	}
}

// A VMM that ignores SIGTERM still has to go away.
func TestStopKillsAStubbornVMM(t *testing.T) {
	p := runPaths(t)
	binary := stubVMM(t, `trap '' TERM; : > "$(echo "$@" | sed 's/.*--api-socket path=//;s/ .*//')"; `+
		`while true; do sleep 1; done`)

	proc, err := launch(binary, testSpec(), p)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := proc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if proc.Running() {
		t.Error("the vmm survived Stop")
	}
}

func TestWaitStoppedTimesOut(t *testing.T) {
	p := runPaths(t)
	binary := stubVMM(t, `: > "$(echo "$@" | sed 's/.*--api-socket path=//;s/ .*//')"; sleep 30`)

	proc, err := launch(binary, testSpec(), p)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = proc.Stop() }()

	if err := proc.WaitStopped(50 * time.Millisecond); err == nil {
		t.Error("a running vmm should not report itself stopped")
	}
}
