package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// process is the Docker implementation of sandbox.Process. The
// hijacked connection from ExecAttach delivers a multiplexed stream;
// stdcopy demuxes it into stdout and stderr, which we pipe to
// caller-readable io.ReadClosers.
//
// Lifecycle:
//   - newProcess starts the pump goroutine (and a stdin-copy goroutine
//     if Stdin was provided).
//   - Calling Close, or Wait reaching natural exit, tears down the
//     attach connection — which unblocks the pump's stdcopy read —
//     closes the pipe writers, and waits for goroutines to drain.
//   - Calling neither leaks the connection and at least one goroutine.
//     The interface contract makes Close mandatory.
type process struct {
	cli    *client.Client
	execID string
	attach client.HijackedResponse

	stdoutR *io.PipeReader
	stderrR *io.PipeReader

	// goroutines tracks the pump and (optional) stdin copier so
	// Close can wait for them after tearing down the connection.
	goroutines sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	doneCh chan struct{} // closed when the pump goroutine has exited
}

func newProcess(cli *client.Client, execID string, attach client.HijackedResponse, stdin io.Reader) *process {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	p := &process{
		cli:     cli,
		execID:  execID,
		attach:  attach,
		stdoutR: stdoutR,
		stderrR: stderrR,
		doneCh:  make(chan struct{}),
	}

	// Forward caller stdin into the attach connection. We half-close
	// on EOF so the inside-the-container process sees stdin EOF.
	if stdin != nil {
		p.goroutines.Add(1)
		go func() {
			defer p.goroutines.Done()
			_, _ = io.Copy(attach.Conn, stdin)
			_ = attach.CloseWrite()
		}()
	}

	p.goroutines.Add(1)
	go func() {
		defer p.goroutines.Done()
		defer close(p.doneCh)

		// stdcopy returns when the attach connection closes (process
		// exit, or our Close call) or on a stream error. Whichever
		// path we take, propagate to the pipe readers so callers
		// reading Stdout/Stderr see a consistent EOF or error.
		_, err := stdcopy.StdCopy(stdoutW, stderrW, attach.Reader)
		// CloseWithError(nil) is equivalent to Close — both unblock
		// readers with io.EOF; passing a non-nil err surfaces it to
		// the reader without an in-band write that could deadlock
		// if no one is reading.
		if err != nil && !errors.Is(err, io.EOF) {
			_ = stdoutW.CloseWithError(err)
			_ = stderrW.CloseWithError(err)
		} else {
			_ = stdoutW.Close()
			_ = stderrW.Close()
		}
	}()

	return p
}

func (p *process) Stdout() io.ReadCloser { return p.stdoutR }
func (p *process) Stderr() io.ReadCloser { return p.stderrR }

// Close tears down the attach connection (unblocking any pump
// goroutine stuck on a stream read) and waits for all worker
// goroutines to exit. Safe to call multiple times.
func (p *process) Close() error {
	p.closeOnce.Do(func() {
		// Closing the hijacked connection unblocks both stdcopy
		// (reading from attach.Reader) and any pending io.Copy of
		// stdin (writing to attach.Conn). Pipe readers will see EOF
		// after the pump exits and closes the writers.
		p.attach.Close()
		p.goroutines.Wait()
		// Also close the reader sides so callers blocked on a slow
		// stdout/stderr read get an immediate EOF rather than
		// waiting for a pipe writer that's already done its work.
		_ = p.stdoutR.Close()
		_ = p.stderrR.Close()
	})
	return p.closeErr
}

// Wait blocks until the process exits naturally, ctx is cancelled,
// or Close is called. Successful Wait implicitly closes the process.
func (p *process) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.doneCh:
		// Natural exit — pump drained the stream. Fall through to
		// the inspect+close path.
	case <-ctx.Done():
		// Caller gave up. Close tears down the connection (which
		// also unblocks the pump goroutine still reading from it).
		_ = p.Close()
		return 0, ctx.Err()
	}

	// Query the daemon for the exit code BEFORE we Close — Close
	// will free the exec handle (Docker keeps it around for inspect
	// while the connection is open).
	inspect, inspectErr := p.cli.ExecInspect(ctx, p.execID, client.ExecInspectOptions{})
	_ = p.Close()
	if inspectErr != nil {
		return 0, fmt.Errorf("docker: exec inspect %s: %w", p.execID, inspectErr)
	}
	if inspect.Running {
		// Shouldn't happen: doneCh closed implies the attach stream
		// ended, which Docker considers the exec done. Guard anyway.
		return 0, errors.New("docker: attach closed but exec still running")
	}
	return inspect.ExitCode, nil
}

// Signal: Docker delivers signals at the container level
// (ContainerKill), not per-exec. Returns sandbox.ErrNotSupported so
// callers can detect this portably. Whole-sandbox shutdown goes
// through Driver.Stop.
func (p *process) Signal(s os.Signal) error {
	return sandbox.ErrNotSupported
}
