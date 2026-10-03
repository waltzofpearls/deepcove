package vmproc

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// guestDialTimeout bounds reaching a guest port. Apple VF never answers a port nobody listens on.
const guestDialTimeout = 5 * time.Second

// DialFunc reaches a port the guest listens on.
type DialFunc func(ctx context.Context, port uint32) (net.Conn, error)

// Relay serves a VM's vsock on a Unix socket, the way Cloud Hypervisor does, from the process that holds
// the VM. DialGuest is its other end.
type Relay struct {
	listener net.Listener
	dial     DialFunc
	done     sync.WaitGroup
}

// ServeGuest starts answering CONNECT requests on socketPath.
func ServeGuest(socketPath string, dial DialFunc) (*Relay, error) {
	if err := checkSocketPath(socketPath); err != nil {
		return nil, err
	}
	_ = RemoveSocket(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("vmproc: listen on %s: %w", socketPath, err)
	}
	relay := &Relay{listener: listener, dial: dial}
	relay.done.Add(1)
	go func() {
		defer relay.done.Done()
		acceptEach(listener, relay.connect)
	}()
	return relay, nil
}

// Close stops taking new connections. Ones already passing bytes end when either side closes.
func (r *Relay) Close() error {
	err := r.listener.Close()
	r.done.Wait()
	return err
}

func (r *Relay) connect(host net.Conn) {
	port, err := readConnect(host)
	if err != nil {
		_ = host.Close()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), guestDialTimeout)
	defer cancel()
	guest, err := r.dial(ctx, port)
	if err != nil {
		_, _ = fmt.Fprintf(host, "ERR %v\n", err)
		_ = host.Close()
		return
	}
	if _, err := fmt.Fprintf(host, "%s %d\n", connectReplyOK, port); err != nil {
		_, _ = host.Close(), guest.Close()
		return
	}
	splice(host, guest)
}

// readConnect reads the "CONNECT <port>" line, giving a peer that never sends one a bounded wait.
func readConnect(conn net.Conn) (uint32, error) {
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return 0, err
	}
	line, err := readLine(conn)
	if err != nil {
		return 0, err
	}
	port, err := strconv.ParseUint(strings.TrimPrefix(line, "CONNECT "), 10, 32)
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		return 0, fmt.Errorf("vmproc: not a CONNECT request: %q", line)
	}
	return uint32(port), conn.SetDeadline(time.Time{})
}

// ForwardFromGuest passes each connection the guest opens on guest to ListenGuest's socket for port,
// until guest is closed.
func ForwardFromGuest(guest net.Listener, socketPath string, port uint32) {
	path := HostPortPath(socketPath, port)
	acceptEach(guest, func(conn net.Conn) {
		host, err := dialWhenListening(path, guestDialTimeout)
		if err != nil {
			_ = conn.Close()
			return
		}
		splice(host, conn)
	})
}

// dialWhenListening retries until something listens on path. At boot the guest can call before the
// host side is up, and the guest treats a refused config fetch as fatal.
func dialWhenListening(path string, wait time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(wait)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil || time.Now().After(deadline) {
			return conn, err
		}
		time.Sleep(pollEvery)
	}
}

// acceptEach hands every connection to handle on its own goroutine until the listener closes.
func acceptEach(listener net.Listener, handle func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handle(conn)
	}
}

// splice passes bytes both ways and closes both ends once either direction ends. Apple VF connections
// cannot half close, so one side finishing is the whole connection finishing.
func splice(host, guest net.Conn) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _, _ = host.Close(), guest.Close() }) }
	go func() {
		_, _ = io.Copy(host, guest)
		closeBoth()
	}()
	_, _ = io.Copy(guest, host)
	closeBoth()
}
