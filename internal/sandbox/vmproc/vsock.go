// Package vmproc talks to a VM that runs in its own host process: start and stop that process, and
// reach the guest's vsock through the process's socket. Cloud Hypervisor's protocol: send
// "CONNECT <port>" to reach the guest, and listen on "<socket>_<port>" for connections the guest opens.
package vmproc

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	handshakeTimeout = 5 * time.Second
	connectReplyOK   = "OK"
	maxReplyBytes    = 64
	// 104 bytes on macOS, 108 on Linux. We take the shorter one for safety.
	maxSocketPath = 104
)

// DialGuest opens a connection to a service the guest is listening on.
func DialGuest(ctx context.Context, socketPath string, port uint32) (net.Conn, error) {
	if err := checkSocketPath(socketPath); err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("vmproc: dial the vsock socket %s: %w", socketPath, err)
	}
	if err := handshake(ctx, conn, port); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func handshake(ctx context.Context, conn net.Conn, port uint32) error {
	deadline := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		return fmt.Errorf("vmproc: request vsock port %d: %w", port, err)
	}
	reply, err := readLine(conn)
	if err != nil {
		return fmt.Errorf("vmproc: read the reply for vsock port %d: %w", port, err)
	}
	if !strings.HasPrefix(reply, connectReplyOK) {
		return fmt.Errorf("vmproc: vsock port %d refused: %s", port, strings.TrimSpace(reply))
	}
	return conn.SetDeadline(time.Time{})
}

// readLine reads a byte at a time: a buffered reader would swallow the guest's first bytes.
func readLine(conn net.Conn) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < maxReplyBytes {
		if _, err := conn.Read(buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return string(line), nil
		}
		line = append(line, buf[0])
	}
	return "", fmt.Errorf("vmproc: the reply never ended")
}

// ListenGuest listens for connections the guest opens to a host port.
func ListenGuest(socketPath string, port uint32) (net.Listener, error) {
	path := HostPortPath(socketPath, port)
	if err := checkSocketPath(path); err != nil {
		return nil, err
	}
	// A crashed VM leaves its socket behind, and bind fails on an existing path.
	_ = RemoveSocket(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("vmproc: listen on %s: %w", path, err)
	}
	return listener, nil
}

func checkSocketPath(path string) error {
	if len(path) > maxSocketPath {
		return fmt.Errorf("vmproc: the socket path is %d bytes, over the %d byte limit: %s",
			len(path), maxSocketPath, path)
	}
	return nil
}

// HostPortPath is where a listener for host port waits, beside the VM's vsock socket.
func HostPortPath(socketPath string, port uint32) string {
	return fmt.Sprintf("%s_%d", socketPath, port)
}
