//go:build unix

package guestexec

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Serve runs one exec to completion on conn: it reads the request, starts the command, relays stdio in
// both directions, applies resizes and signals, and finally reports the exit status. It always tries to
// send an exit frame so the host never has to guess why a command ended.
//
// It is not gated to Linux because the pieces it uses work on any Unix, which lets the full host against
// guest round trip be tested on the development machine rather than only inside a VM.
func Serve(conn net.Conn) error { return serveWith(conn, nil) }

// ChildWaiter is how a host that is PID 1 keeps ownership of child processes.
//
// An init must reap orphans with wait4(-1), which collects the status of ANY child, including the ones
// started here. cmd.Wait then loses the race and returns ECHILD ("no child processes"), so the exec
// reports a bogus -1 and the caller cannot tell what happened. Handing both operations to the reaper
// makes it the only waiter, and it routes each status back to whoever started the process.
type ChildWaiter interface {
	// Start runs start (which must launch cmd) with reaping held off, then tracks cmd's pid. Holding
	// reaping across both closes the window where a short lived child exits before it is tracked.
	Start(cmd *exec.Cmd, start func() error) error
	// Wait returns the exit code the waiter collected for cmd.
	Wait(cmd *exec.Cmd) (int, error)
}

// serveWith is Serve with an explicit waiter. A nil waiter waits normally, which is right for anything
// that is not PID 1, including the tests that drive this over a pipe.
func serveWith(conn net.Conn, waiter ChildWaiter) error {
	defer func() { _ = conn.Close() }()

	req, err := ReadRequest(conn)
	if err != nil {
		return err
	}
	if len(req.Argv) == 0 {
		return writeExit(conn, &sync.Mutex{}, -1, errors.New("guestexec: empty argv"))
	}

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...) //nolint:gosec // argv comes from the trusted host side
	cmd.Env = append(os.Environ(), envList(req.Env)...)
	cmd.Dir = req.Dir

	var writeMu sync.Mutex
	if req.Tty {
		return serveTTY(conn, &writeMu, cmd, req, waiter)
	}
	return servePipes(conn, &writeMu, cmd, waiter)
}

// serveTTY runs the command on a pseudo terminal. A TTY merges stderr into stdout exactly as a real
// terminal does, so everything comes back as stdout frames and Stderr stays empty on the host.
func serveTTY(conn net.Conn, writeMu *sync.Mutex, cmd *exec.Cmd, req ExecRequest, waiter ChildWaiter) error {
	size := &pty.Winsize{Cols: req.Cols, Rows: req.Rows}
	if size.Cols == 0 || size.Rows == 0 {
		size = &pty.Winsize{Cols: 80, Rows: 24} // a sane default beats a 0x0 terminal
	}
	var ptmx *os.File
	err := startChild(waiter, cmd, func() error {
		var perr error
		ptmx, perr = pty.StartWithSize(cmd, size)
		return perr
	})
	if err != nil {
		return writeExit(conn, writeMu, -1, fmt.Errorf("guestexec: start with pty: %w", err))
	}
	defer func() { _ = ptmx.Close() }()

	// Control runs in the background: it blocks until the host hangs up, while this goroutine has to
	// reach the exit frame the host is waiting for.
	go control(conn, cmd, childIO{
		stdin: ptmx,
		// A terminal has no half close; the child sees EOF only when the pty itself goes away.
		onHangup: func() { _ = ptmx.Close() },
		resizer:  ptmx,
	})

	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		relay(conn, writeMu, FrameStdout, ptmx)
	}()

	code, _ := waitChild(waiter, cmd)
	// The child is gone. Linux returns EIO from the pty master at this point, but macOS can block there
	// forever, so set a short deadline instead: it still lets already buffered output drain, then ends
	// the relay on both platforms.
	_ = ptmx.SetReadDeadline(time.Now().Add(ptyDrainGrace))
	<-relayed
	return writeExit(conn, writeMu, code, nil)
}

// ptyDrainGrace is how long the pty relay may keep draining after the child exits, before the read is
// forced to return. Long enough for buffered output, short enough not to stall the exit.
const ptyDrainGrace = 200 * time.Millisecond

// servePipes runs the command without a terminal, keeping stdout and stderr separate.
func servePipes(conn net.Conn, writeMu *sync.Mutex, cmd *exec.Cmd, waiter ChildWaiter) error {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return writeExit(conn, writeMu, -1, fmt.Errorf("guestexec: stdin pipe: %w", err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return writeExit(conn, writeMu, -1, fmt.Errorf("guestexec: stdout pipe: %w", err))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return writeExit(conn, writeMu, -1, fmt.Errorf("guestexec: stderr pipe: %w", err))
	}
	// Give the command its own process group so signals and cleanup reach everything it spawns. A shell
	// like `sh -c "sleep 30"` often FORKS rather than execs, so signalling just the direct child leaves
	// the real work running, still holding the output pipes open, which stalls this exec until it ends.
	// The TTY path does not need this: pty.StartWithSize already puts the child in a new session.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := startChild(waiter, cmd, cmd.Start); err != nil {
		return writeExit(conn, writeMu, -1, fmt.Errorf("guestexec: start: %w", err))
	}

	go control(conn, cmd, childIO{
		stdin:      stdin,
		closeStdin: stdin.Close,
		// If the host disappears, give the child EOF so it can finish rather than hang.
		onHangup: func() { _ = stdin.Close() },
	})

	// Drain both streams to EOF, which happens when the child exits, before reporting status so no
	// output is lost. StdoutPipe and StderrPipe must be fully read before Wait.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); relay(conn, writeMu, FrameStdout, stdout) }()
	go func() { defer wg.Done(); relay(conn, writeMu, FrameStderr, stderr) }()
	wg.Wait()

	// A non nil error here means the command never ran to completion. Dropping it left the host with a
	// bare "-1" and nothing to go on.
	code, err := waitChild(waiter, cmd)
	if err != nil {
		return writeExit(conn, writeMu, code, fmt.Errorf("guestexec: wait: %w", err))
	}
	return writeExit(conn, writeMu, code, nil)
}

// childIO is how the control loop reaches the running child: where to write host stdin, how to give it
// EOF, how to react when the host disconnects, and the pty master to resize (nil without a TTY).
type childIO struct {
	stdin      io.Writer
	closeStdin func() error
	onHangup   func()
	resizer    *os.File
}

// control reads host bound frames until the connection ends: stdin data, terminal resizes, and signals.
// It runs in its own goroutine because it blocks until the host hangs up, while the caller must get to
// the exit frame the host is waiting for.
func control(conn net.Conn, cmd *exec.Cmd, cio childIO) {
	for {
		t, payload, err := ReadFrame(conn)
		if err != nil {
			cio.onHangup()
			// The host is gone, so nothing will ever read this command's output. Kill it rather than
			// leave an orphan running inside the VM for the rest of the session. The agent itself is
			// started by the guest init, not by an exec, so this only reaps abandoned exec commands.
			signalGroup(cmd, syscall.SIGKILL)
			return
		}
		switch t {
		case FrameStdin:
			if _, werr := cio.stdin.Write(payload); werr != nil {
				return
			}
		case FrameStdinClose:
			if cio.closeStdin != nil {
				_ = cio.closeStdin()
			}
		case FrameResize:
			cols, rows, derr := DecodeResize(payload)
			if derr != nil || cio.resizer == nil {
				continue
			}
			_ = pty.Setsize(cio.resizer, &pty.Winsize{Cols: cols, Rows: rows})
		case FrameSignal:
			sig, derr := DecodeSignal(payload)
			if derr != nil {
				continue
			}
			signalGroup(cmd, syscall.Signal(sig))
		default:
			// Guest bound frame types and anything unknown are ignored, so a newer host stays compatible.
		}
	}
}

// signalGroup sends sig to the child's whole process group, falling back to the direct child if the
// group cannot be resolved.
//
// Targeting the group matters because a shell frequently forks instead of execing: `sh -c "sleep 30"`
// leaves `sleep` as a separate process that inherits the output pipes. Signalling only the shell would
// leave that process running and the pipes open, so the exec would never finish.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		if err := syscall.Kill(-pgid, sig); err == nil {
			return
		}
	}
	_ = cmd.Process.Signal(sig)
}

// relay copies one output stream to the host as frames of the given type.
func relay(conn net.Conn, writeMu *sync.Mutex, t FrameType, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			writeMu.Lock()
			werr := WriteFrame(conn, t, buf[:n])
			writeMu.Unlock()
			if werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// writeExit reports the final status. cause, when set, describes a failure to run the command at all, as
// opposed to the command itself exiting non zero.
func writeExit(conn net.Conn, writeMu *sync.Mutex, code int, cause error) error {
	if cause != nil {
		// Surface the reason on stderr before the exit frame, so the host sees why rather than a bare code.
		writeMu.Lock()
		_ = WriteFrame(conn, FrameStderr, []byte(cause.Error()+"\n"))
		writeMu.Unlock()
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := WriteFrame(conn, FrameExit, EncodeExit(int32(code))); err != nil {
		return err
	}
	return cause
}

// startChild launches cmd, through the waiter when one owns child processes.
func startChild(waiter ChildWaiter, cmd *exec.Cmd, start func() error) error {
	if waiter != nil {
		return waiter.Start(cmd, start)
	}
	return start()
}

// waitChild collects cmd's exit code. The error is non nil only when the outcome could not be
// determined, never for a command that simply exited non zero.
func waitChild(waiter ChildWaiter, cmd *exec.Cmd) (int, error) {
	if waiter != nil {
		return waiter.Wait(cmd)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return exitCodeOf(err), err
	}
	return exitCodeOf(err), nil
}

// exitCodeOf turns the error from Wait into a status: 0 for success, the child's code when it exited non
// zero, and -1 when the outcome could not be determined.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
