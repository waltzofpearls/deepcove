package vmproc

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortTempDir keeps socket paths inside the platform limit; t.TempDir() is already too long on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeVMM reads the CONNECT line, answers, then echoes.
func fakeVMM(t *testing.T, reply func(port string) string) string {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "vsock.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := readLine(conn)
				if err != nil {
					return
				}
				port := strings.TrimPrefix(line, "CONNECT ")
				if _, err := io.WriteString(conn, reply(port)); err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return path
}

func TestDialGuest(t *testing.T) {
	path := fakeVMM(t, func(port string) string { return "OK " + port + "\n" })

	conn, err := DialGuest(context.Background(), path, 1024)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The connection must be clean after the handshake.
	if _, err := io.WriteString(conn, "hello\n"); err != nil {
		t.Fatal(err)
	}
	got, err := readLine(conn)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// Nothing listening in the guest is ordinary during boot, and has to read as an error.
func TestDialGuestRefused(t *testing.T) {
	path := fakeVMM(t, func(string) string { return "ERROR 2\n" })

	if _, err := DialGuest(context.Background(), path, 1024); err == nil {
		t.Error("a refused port should fail")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Errorf("the error should say it was refused, got: %v", err)
	}
}

func TestDialGuestNoSocket(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "not-there.sock")
	if _, err := DialGuest(context.Background(), missing, 1024); err == nil {
		t.Error("dialing a socket that does not exist should fail")
	}
}

func TestDialGuestHonoursTheContext(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "vsock.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		<-time.After(time.Minute)
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := DialGuest(ctx, path, 1024); err == nil {
		t.Fatal("a silent VMM should not produce a working connection")
	}
	if elapsed := time.Since(start); elapsed > handshakeTimeout {
		t.Errorf("waited %s, longer than the context allowed", elapsed)
	}
}

func TestListenGuest(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "vsock.sock")
	listener, err := ListenGuest(socket, 1026)
	if err != nil {
		t.Fatalf("ListenGuest: %v", err)
	}
	defer func() { _ = listener.Close() }()

	conn, err := net.Dial("unix", fmt.Sprintf("%s_%d", socket, 1026))
	if err != nil {
		t.Fatalf("dial as the VMM would: %v", err)
	}
	defer func() { _ = conn.Close() }()

	accepted, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	_ = accepted.Close()
}

func TestListenGuestReplacesAStaleSocket(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "vsock.sock")
	stale, err := net.Listen("unix", fmt.Sprintf("%s_%d", socket, 1026))
	if err != nil {
		t.Fatal(err)
	}
	_ = stale.Close()

	listener, err := ListenGuest(socket, 1026)
	if err != nil {
		t.Fatalf("ListenGuest over a stale socket: %v", err)
	}
	_ = listener.Close()
}

func TestRemoveSocketIgnoresMissing(t *testing.T) {
	if err := RemoveSocket(filepath.Join(shortTempDir(t), "gone.sock")); err != nil {
		t.Errorf("removing a socket that is not there should be fine, got %v", err)
	}
}

func TestListenGuestRejectsALongPath(t *testing.T) {
	long := filepath.Join(shortTempDir(t), strings.Repeat("d", 100), "vsock.sock")
	_, err := ListenGuest(long, 1026)
	if err == nil {
		t.Fatal("a socket path over the limit should fail")
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("the error should explain the limit, got: %v", err)
	}
}
