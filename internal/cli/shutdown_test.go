package cli

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDrainServers(t *testing.T) {
	cases := []struct {
		name string
		// dial opens a connection that never sends a request, like a browser holding a spare one.
		dial bool
	}{
		{name: "no connections", dial: false},
		{name: "a connection that never sends a request", dial: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
			go func() { _ = srv.Serve(ln) }()

			if tc.dial {
				conn, err := net.Dial("tcp", ln.Addr().String())
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				defer func() { _ = conn.Close() }()
			}

			start := time.Now()
			if err := drainServers(200*time.Millisecond, slog.New(slog.DiscardHandler), srv); err != nil {
				t.Fatalf("drain: %v", err)
			}
			if waited := time.Since(start); waited > 2*time.Second {
				t.Fatalf("drain took %v, expected it to give up on idle connections quickly", waited)
			}
		})
	}
}

func TestDumpStacks(t *testing.T) {
	dir := t.TempDir()
	path, err := dumpStacks(dir)
	if err != nil {
		t.Fatalf("dumpStacks: %v", err)
	}
	if want := filepath.Join(dir, "logs", "shutdown-stacks.txt"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The point of the file is naming what is stuck, so it has to contain real stacks.
	if !strings.Contains(string(body), "goroutine ") {
		t.Errorf("file has no goroutine stacks: %q", string(body)[:min(120, len(body))])
	}
}
