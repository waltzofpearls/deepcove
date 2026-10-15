// Package middleware holds the per-front-door HTTP gates for the deepcove API.
// In `deepcove start` the same mux is served on three listeners, each wrapped
// with the gate matching its trust model:
//
//   - Unix socket: ungated; filesystem permissions are the boundary (CLI).
//   - Local TCP: LocalGate here (the same user's browser on this machine).
//   - Tailnet: owner-gated via WhoIs (in package connect).
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/localpeer"
)

// LocalConnContext checks once per connection whether it comes from the OS user deepcove runs as, and
// keeps the answer for LocalGate. Set it as the local server's ConnContext.
func LocalConnContext(ctx context.Context, conn net.Conn) context.Context {
	same, err := localpeer.SameUser(conn)
	if err != nil {
		slog.Warn("local dashboard: could not tell who is connecting, refusing", "remote", conn.RemoteAddr(), "err", err)
	}
	return context.WithValue(ctx, sameUserKey{}, same && err == nil)
}

type sameUserKey struct{}

// LocalGate restricts the local TCP front door (127.0.0.1) to the browser of the user who runs
// deepcove. Three checks:
//
//   - Same user: a connection from any other OS user on this machine is refused. The kernel says who
//     owns each connection (see LocalConnContext), so there is no token anyone could take.
//   - Host allowlist: the request Host must be loopback (localhost / 127.x / ::1) or an explicitly
//     configured host. This defeats DNS rebinding, where a malicious site points its name at
//     127.0.0.1: its requests still carry Host: evil.example, which we reject.
//   - Cross origin: a page from another site, or another port on 127.0.0.1, runs in the same user's
//     browser and so passes the first check. It may not call the API at all, terminal included.
//
// The Unix socket (CLI) and the owner-gated tailnet never use this gate.
func LocalGate(next http.Handler, allowedHosts []string) http.Handler {
	next = http.NewCrossOriginProtection().Handler(next)
	allow := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		if host != "" {
			allow[strings.ToLower(host)] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if same, _ := r.Context().Value(sameUserKey{}).(bool); !same {
			http.Error(w, "forbidden: this deepcove only answers the user who runs it", http.StatusForbidden)
			return
		}
		if !hostAllowed(r.Host, allow) {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		if crossOriginAPI(r) {
			http.Error(w, "forbidden: cross origin request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed checks the request Host (sans port) against loopback and the
// configured allowlist.
func hostAllowed(requestHost string, allow map[string]struct{}) bool {
	host := normalizeHost(requestHost)
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	_, ok := allow[host]
	return ok
}

// normalizeHost strips the port and lowercases a Host header for comparison.
func normalizeHost(requestHost string) string {
	host := requestHost
	if hostOnly, _, err := net.SplitHostPort(requestHost); err == nil {
		host = hostOnly
	}
	return strings.ToLower(strings.Trim(host, "[]")) // strip IPv6 brackets if present
}
