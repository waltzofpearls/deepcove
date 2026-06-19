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
// hijacked connection from ExecAttach delivers either a multiplexed
// stream (no TTY) demuxed by stdcopy, or a raw stream (TTY) we copy
// straight into stdout. The pump goroutine handles whichever shape
// matches the exec's TTY flag.
//
// Lifecycle:
//   - newProcess starts the pump goroutine (and, if ExecSpec.Stdin was non-nil, a stdin-copy
//     goroutine).
//   - Close (or successful Wait, which Closes internally) tears down the attach connection, which
//     unblocks the pump's read, and waits for goroutines to drain.
//   - Calling neither leaks the connection. The interface contract makes Close mandatory.
type process struct {
	cli    *client.Client
	execID string
	attach client.HijackedResponse
	tty    bool

	stdoutR *io.PipeReader
	stderrR *io.PipeReader

	goroutines sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	doneCh chan struct{} // closed when the pump goroutine has exited
}

func newProcess(cli *client.Client, execID string, attach client.HijackedResponse, stdin io.Reader, tty bool) *process {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	p := &process{
		cli:     cli,
		execID:  execID,
		attach:  attach,
		tty:     tty,
		stdoutR: stdoutR,
		stderrR: stderrR,
		doneCh:  make(chan struct{}),
	}

	// Forward a caller-provided stdin into the attach connection.
	// Interactive callers can leave ExecSpec.Stdin nil and write via
	// Process.Stdin() instead.
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

		var err error
		if tty {
			// TTY exec: the stream is raw terminal output; stdcopy
			// would misinterpret the bytes. We don't have a way to
			// distinguish stdout from stderr in this mode (Docker
			// merges them), so everything lands on stdout and stderr
			// closes immediately so callers don't block.
			_ = stderrW.Close()
			_, err = io.Copy(stdoutW, attach.Reader)
		} else {
			_, err = stdcopy.StdCopy(stdoutW, stderrW, attach.Reader)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			_ = stdoutW.CloseWithError(err)
			if !tty {
				_ = stderrW.CloseWithError(err)
			}
		} else {
			_ = stdoutW.Close()
			if !tty {
				_ = stderrW.Close()
			}
		}
	}()

	return p
}

func (p *process) Stdout() io.ReadCloser { return p.stdoutR }
func (p *process) Stderr() io.ReadCloser { return p.stderrR }

// Stdin returns a writer that forwards into the running exec's
// stdin. Writes after Close return io.ErrClosedPipe. Calling
// Stdin().Close() half-closes the input stream (the inside-container
// process sees EOF on its stdin) without affecting Stdout/Stderr.
//
// If the caller already provided ExecSpec.Stdin at create time, the returned writer races with the
// spawned copier; don't use both.
func (p *process) Stdin() io.WriteCloser {
	return &stdinWriter{p: p}
}

type stdinWriter struct {
	p *process
}

func (w *stdinWriter) Write(b []byte) (int, error) {
	return w.p.attach.Conn.Write(b)
}

func (w *stdinWriter) Close() error {
	// Half-close: tell the daemon we're done writing while keeping
	// the read side open for stdout/stderr drain.
	return w.p.attach.CloseWrite()
}

// Resize forwards new TTY dimensions to the running exec. Only
// meaningful when the exec was created with Tty=true; non-TTY execs
// silently no-op (Docker doesn't error in that case either).
func (p *process) Resize(cols, rows uint16) error {
	_, err := p.cli.ExecResize(context.Background(), p.execID, client.ExecResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
	return err
}

// Close tears down the attach connection (unblocking any pump
// goroutine stuck on a stream read) and waits for all worker
// goroutines to exit. Safe to call multiple times.
func (p *process) Close() error {
	p.closeOnce.Do(func() {
		p.attach.Close()
		p.goroutines.Wait()
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
	case <-ctx.Done():
		_ = p.Close()
		return 0, ctx.Err()
	}

	inspect, inspectErr := p.cli.ExecInspect(ctx, p.execID, client.ExecInspectOptions{})
	_ = p.Close()
	if inspectErr != nil {
		return 0, fmt.Errorf("docker: exec inspect %s: %w", p.execID, inspectErr)
	}
	if inspect.Running {
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
