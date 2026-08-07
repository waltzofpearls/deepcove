package cli

import (
	"log/slog"
	"net"
	"net/http"
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
