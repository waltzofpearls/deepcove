package vm

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/guestctl"
)

// fakeBackend hands out fakeMachines, so the driver and the guest services can be exercised with no
// hypervisor at all.
type fakeBackend struct {
	mu       sync.Mutex
	machines []*fakeMachine
	bootErr  error
}

func (b *fakeBackend) Boot(Spec) (Machine, error) {
	if b.bootErr != nil {
		return nil, b.bootErr
	}
	m := newFakeMachine()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.machines = append(b.machines, m)
	return m, nil
}

func (b *fakeBackend) Version() (string, error) {
	return "fake hypervisor", nil
}

// fakeMachine answers vsock dials from in memory handlers and hands out in memory listeners, which is
// the whole of what a guest looks like to this package.
type fakeMachine struct {
	mu sync.Mutex
	// guests are the services the guest is listening on, keyed by port.
	guests map[uint32]func(net.Conn)
	// hosted are the ports the host is listening on, keyed by port, so a test can dial in as the guest.
	hosted  map[uint32]*pipeListener
	running bool
	stops   int
	// dials counts what the host asked of the guest, so a test can tell a polite stop from a forced one.
	dials int
	// stopDelay makes Stop take time, the way a guest that will not answer does.
	stopDelay time.Duration
	// stubborn makes the guest accept a shutdown and never halt.
	stubborn bool
	console  *pipeConsole
}

func newFakeMachine() *fakeMachine {
	return &fakeMachine{
		guests:  map[uint32]func(net.Conn){},
		hosted:  map[uint32]*pipeListener{},
		running: true,
		console: &pipeConsole{},
	}
}

// serveGuest registers a service the guest is listening on.
func (m *fakeMachine) serveGuest(port uint32, handler func(net.Conn)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guests[port] = handler
}

// dialHost connects to a port the host is listening on, the way a booted guest would.
func (m *fakeMachine) dialHost(port uint32) (net.Conn, error) {
	m.mu.Lock()
	listener := m.hosted[port]
	m.mu.Unlock()
	if listener == nil {
		return nil, errors.New("nothing is listening on that port")
	}
	return listener.dial()
}

func (m *fakeMachine) Dial(_ context.Context, port uint32) (net.Conn, error) {
	m.mu.Lock()
	handler, ok := m.guests[port]
	m.dials++
	m.mu.Unlock()
	if !ok {
		return nil, errors.New("connection refused")
	}
	ours, theirs := net.Pipe()
	go handler(theirs)
	return ours, nil
}

func (m *fakeMachine) Listen(port uint32) (net.Listener, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	listener := newPipeListener()
	m.hosted[port] = listener
	return listener, nil
}

func (m *fakeMachine) Console() io.ReadWriteCloser {
	return m.console
}

func (m *fakeMachine) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *fakeMachine) Stop() error {
	m.mu.Lock()
	delay := m.stopDelay
	m.mu.Unlock()
	time.Sleep(delay)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops++
	m.running = false
	return nil
}

func (m *fakeMachine) WaitStopped(timeout time.Duration) error {
	m.mu.Lock()
	stubborn := m.stubborn
	m.mu.Unlock()
	if stubborn {
		// A guest that took the shutdown and then never halted, which is what burns the budget.
		time.Sleep(timeout)
		return errors.New("the guest did not halt")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	return nil
}

// answerControl makes the guest answer the control service, which is what readiness polls for.
func (m *fakeMachine) answerControl() {
	m.serveGuest(guestctl.Port, func(conn net.Conn) {
		_ = guestctl.Handler{Shutdown: func() error { return nil }}.Serve(conn)
	})
}

// pipeListener is a net.Listener over net.Pipe, so a test can be both ends of a vsock port.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) dial() (net.Conn, error) {
	ours, theirs := net.Pipe()
	select {
	case l.conns <- theirs:
		return ours, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return pipeAddr{}
}

type pipeAddr struct{}

func (pipeAddr) Network() string {
	return "pipe"
}

func (pipeAddr) String() string {
	return "pipe"
}

// pipeConsole stands in for the guest's serial console: a test writes to it, the drain reads it.
type pipeConsole struct {
	mu     sync.Mutex
	buf    []byte
	closed bool
}

func (c *pipeConsole) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *pipeConsole) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.buf) > 0 {
			n := copy(p, c.buf)
			c.buf = c.buf[n:]
			c.mu.Unlock()
			return n, nil
		}
		done := c.closed
		c.mu.Unlock()
		if done {
			return 0, io.EOF
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *pipeConsole) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
