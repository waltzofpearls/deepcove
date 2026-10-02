package guestexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Client is the host end of one exec. It implements sandbox.Process, so the session layer drives a VM
// backed command exactly as it drives any other.
//
// It takes a net.Conn rather than dialing itself, which keeps it platform neutral: the Apple VF driver
// hands it a vsock connection, and tests hand it one half of a net.Pipe.
type Client struct {
	conn net.Conn

	stdout   *io.PipeWriter
	stderr   *io.PipeWriter
	stdoutR  *io.PipeReader
	stderrR  *io.PipeReader
	writeMu  sync.Mutex // serializes frames written to conn
	done     chan struct{}
	closeOne sync.Once

	mu       sync.Mutex
	exitCode int32
	exitErr  error
}

var _ sandbox.Process = (*Client)(nil)

// Start opens an exec on conn: it sends the request and begins pumping frames from the guest. The Client
// owns conn from this point and closes it on Close or when the guest reports exit.
func Start(conn net.Conn, req ExecRequest) (*Client, error) {
	if err := WriteRequest(conn, req); err != nil {
		return nil, err
	}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	c := &Client{
		conn:     conn,
		stdout:   outW,
		stderr:   errW,
		stdoutR:  outR,
		stderrR:  errR,
		done:     make(chan struct{}),
		exitCode: -1,
	}
	go c.pump()
	return c, nil
}

// pump reads frames from the guest until the stream ends, fanning stdout and stderr into their pipes and
// recording the exit status. It is the only reader of conn.
func (c *Client) pump() {
	defer close(c.done)
	for {
		t, payload, err := ReadFrame(c.conn)
		if err != nil {
			c.finish(err)
			return
		}
		switch t {
		case FrameStdout:
			if _, werr := c.stdout.Write(payload); werr != nil {
				c.finish(werr)
				return
			}
		case FrameStderr:
			if _, werr := c.stderr.Write(payload); werr != nil {
				c.finish(werr)
				return
			}
		case FrameExit:
			code, derr := DecodeExit(payload)
			if derr != nil {
				c.finish(derr)
				return
			}
			c.setExit(code, nil)
			c.closeStreams(nil)
			return
		default:
			// Unknown or host bound frame types are ignored rather than fatal, so a newer guest can add
			// frames without breaking an older host.
		}
	}
}

// finish records why the stream ended. A clean EOF before an exit frame means the guest went away
// without reporting status, which the caller needs to hear about rather than see as exit 0.
func (c *Client) finish(err error) {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		err = fmt.Errorf("guestexec: connection closed before the process reported an exit status")
	}
	c.setExit(-1, err)
	c.closeStreams(err)
}

func (c *Client) setExit(code int32, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exitErr == nil && c.exitCode == -1 {
		c.exitCode, c.exitErr = code, err
	}
}

func (c *Client) closeStreams(err error) {
	_ = c.stdout.CloseWithError(err)
	_ = c.stderr.CloseWithError(err)
}

// send writes one frame to the guest, serialized because stdin writes, resizes, and signals can race.
func (c *Client) send(t FrameType, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteFrame(c.conn, t, payload)
}

// Stdout and Stderr are the guest's streams. With Tty set the guest merges stderr into stdout, the same
// as a real terminal, and Stderr stays empty.
func (c *Client) Stdout() io.ReadCloser { return c.stdoutR }
func (c *Client) Stderr() io.ReadCloser { return c.stderrR }

// Stdin returns a writer that frames whatever is written to it as stdin for the guest process.
func (c *Client) Stdin() io.WriteCloser { return &stdinWriter{c: c} }

// Wait blocks until the guest reports an exit status, ctx is cancelled, or the connection drops.
func (c *Client) Wait(ctx context.Context) (int, error) {
	select {
	case <-c.done:
		c.mu.Lock()
		code, err := c.exitCode, c.exitErr
		c.mu.Unlock()
		_ = c.Close()
		return int(code), err
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

// Signal forwards a Unix signal to the guest process.
func (c *Client) Signal(s os.Signal) error {
	sig, ok := s.(syscall.Signal)
	if !ok {
		return fmt.Errorf("guestexec: %v is not a Unix signal: %w", s, sandbox.ErrNotSupported)
	}
	return c.send(FrameSignal, EncodeSignal(int32(sig)))
}

// Resize tells a TTY exec its terminal changed size. Meaningless without Tty, where the guest ignores it.
func (c *Client) Resize(cols, rows uint16) error {
	return c.send(FrameResize, EncodeResize(cols, rows))
}

// Close abandons the exec and releases the connection. Safe to call more than once.
func (c *Client) Close() error {
	var err error
	c.closeOne.Do(func() {
		err = c.conn.Close()
		c.closeStreams(nil)
	})
	return err
}

// stdinWriter frames writes as stdin. Closing it tells the guest to close the child's stdin, which is
// how an interactive command sees EOF, without closing the whole connection.
type stdinWriter struct {
	c    *Client
	once sync.Once
}

func (w *stdinWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.c.send(FrameStdin, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *stdinWriter) Close() error {
	var err error
	w.once.Do(func() { err = w.c.send(FrameStdinClose, nil) })
	return err
}
