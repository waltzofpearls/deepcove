package vm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// errGuestSilent is returned in place of dialing a machine whose recent dials went unanswered.
var errGuestSilent = errors.New("vm: the guest is not answering on vsock")

// Guest is a booted machine plus everything the host runs for it: the services the guest dials in to,
// the console drain, and the dial policy that keeps a dead guest from stalling callers.
//
// A Backend hands back a Machine; the driver works with a Guest.
type Guest struct {
	machine Machine
	logger  *slog.Logger

	consoleLog *consoleBuffer
	dial       dialBreaker
	// releaseOnce guards the teardown: the polite stop and the forced one can both reach it.
	releaseOnce sync.Once
	releaseErr  error

	// closers hold what outlives a single call: the console log file, and the service listeners. Guarded
	// because a shutdown can land while the VM is still being given its services.
	closersMu sync.Mutex
	closers   []io.Closer
	released  bool
}

// NewGuest wraps a freshly booted machine. Backends call it from Boot's caller, not from Boot itself, so
// a backend never has to know about the host side services.
func NewGuest(m Machine, logger *slog.Logger) *Guest {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Guest{machine: m, logger: logger}
}

// Console returns the guest console stream.
func (g *Guest) Console() io.ReadWriteCloser {
	return g.machine.Console()
}

// Unresponsive reports that recent dials went unanswered.
func (g *Guest) Unresponsive() bool {
	return !g.dial.allow(time.Now())
}

// Stop asks the guest to power itself off, then tears down the machine and everything held for it.
//
// The polite shutdown comes first because it is the guest's only chance to hand back credentials the
// agent refreshed. The backend's Stop is the hard stop underneath it.
func (g *Guest) Stop() error {
	return errors.Join(g.Shutdown(), g.release())
}

// ForceStop tears the machine down without asking the guest, which loses whatever it would hand back.
func (g *Guest) ForceStop() error {
	return g.release()
}

func (g *Guest) release() error {
	g.releaseOnce.Do(func() {
		g.closersMu.Lock()
		g.released = true
		closers := g.closers
		g.closers = nil
		g.closersMu.Unlock()

		// Outside the lock: closing a listener can block, and keep must stay callable meanwhile.
		for _, c := range closers {
			g.releaseErr = errors.Join(g.releaseErr, c.Close())
		}
		g.releaseErr = errors.Join(g.releaseErr, g.machine.Stop())
	})
	return g.releaseErr
}

// connect dials one of the guest's services, refusing when recent dials went unanswered and giving up
// when ctx does.
func (g *Guest) connect(ctx context.Context, port uint32) (net.Conn, error) {
	if !g.dial.allow(time.Now()) {
		return nil, errGuestSilent
	}
	conn, err := g.probe(ctx, port)
	if err != nil {
		if ctx.Err() != nil && g.dial.recordMissed(time.Now()) {
			g.logger.Warn("the guest stopped answering on vsock", "port", port)
		}
		return nil, err
	}
	g.dial.recordAnswered()
	return conn, nil
}

// probe dials without consulting the breaker, for the boot readiness poll.
//
// A guest that has not come up yet has not stopped answering, and counting its silence would hold off
// the very polls waiting for it: three missed dials arm a 30 second hold off, well inside the 20 second
// boot budget, and the VM would be destroyed as failed while it was still starting.
func (g *Guest) probe(ctx context.Context, port uint32) (net.Conn, error) {
	if !g.machine.Running() {
		return nil, errors.New("vm: the guest is not running")
	}
	return g.machine.Dial(ctx, port)
}

// keep registers something to close when the guest goes away. Handed one after the guest is already
// gone, it closes it now: nothing else ever would.
func (g *Guest) keep(c io.Closer) {
	g.closersMu.Lock()
	if g.released {
		g.closersMu.Unlock()
		_ = c.Close()
		return
	}
	g.closers = append(g.closers, c)
	g.closersMu.Unlock()
}

// openConsoleLog opens the file a guest's console is copied into, or nil when there is no path.
func openConsoleLog(path string) *os.File {
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return file
}
