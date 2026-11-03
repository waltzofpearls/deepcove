// Package middleware holds the per-front-door HTTP gates for the deepcove API.
// In `deepcove start` the same mux is served on three listeners, each wrapped
// with the gate matching its trust model:
//
//   - Unix socket: ungated; filesystem permissions are the boundary (CLI).
//   - Local TCP: LocalGate here (the same user's browser on this machine, unlocked by `deepcove open`).
//   - Tailnet: owner-gated via WhoIs (in package connect).
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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

// LocalGate restricts the local TCP front door (127.0.0.1) to the browsers of the user who runs
// deepcove. Four checks:
//
//   - Same user: a connection from any other OS user on this machine is refused, whatever it sends. The
//     OS says who owns each connection (see LocalConnContext), so a cookie that reached another user is
//     no use to them.
//   - Host allowlist: the request Host must be loopback (localhost / 127.x / ::1) or an explicitly
//     configured host. This defeats DNS rebinding, where a malicious site points its name at
//     127.0.0.1: its requests still carry Host: evil.example, which we reject.
//   - Cross origin: a page from another site, or another port on 127.0.0.1, may not call the API at
//     all, terminal included.
//   - Unlocked: the API answers only a browser holding the cookie from /unlock. The page itself loads
//     for anyone, so a locked browser sees a screen that says how to unlock it.
//
// A page on another local name, like localhost, is sent to localURL first, so the cookie and the
// page always share one origin. The Unix socket (CLI) and the owner-gated tailnet never use this gate.
//
// Accepted: while deepcove is stopped, another user on this machine can take its port and get a page
// into this user's browser. Its script can act as this user once deepcove is back, and a service worker
// it leaves behind keeps that going. VS Code's serve-web has the same gap. Closing it takes a new
// address on every start, which would lock the browser after each restart.
func LocalGate(next http.Handler, allowedHosts []string, localURL *url.URL, login *LocalLogin) http.Handler {
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
		isAPI := strings.HasPrefix(r.URL.Path, "/api/")
		// A cookie belongs to one host, so a page opened on localhost moves to localURL to unlock.
		if !isAPI && r.Method == http.MethodGet && !isLocalURLHost(r.Host, localURL) {
			target := *r.URL
			target.Scheme = localURL.Scheme
			target.Host = localURL.Host
			http.Redirect(w, r, target.String(), http.StatusFound)
			return
		}
		if r.URL.Path == UnlockPath {
			login.unlock(w, r)
			return
		}
		hasSignedIn := login.hasSignedIn(r)
		if isAPI && !hasSignedIn {
			writeResponseLocked(w)
			return
		}
		if hasSignedIn && r.Header.Get("Sec-Fetch-Dest") == "document" {
			login.setCookie(w)
		}
		next.ServeHTTP(w, r)
	})
}

// LocalURL is the one URL every browser page uses, so the cookie always works. A wildcard listen address
// becomes 127.0.0.1.
func LocalURL(listenAddr string) *url.URL {
	listenHost, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return &url.URL{
			Scheme: "http",
			Host:   listenAddr,
			Path:   "/",
		}
	}
	switch listenHost {
	case "", "0.0.0.0", "::":
		listenHost = "127.0.0.1"
	}
	return &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(listenHost, port),
		Path:   "/",
	}
}

// isLocalURLHost is true when a request's Host names localURL. A browser leaves out port 80, and
// it writes an IP its own way, so two IPs are compared as addresses.
func isLocalURLHost(requestHost string, localURL *url.URL) bool {
	requestAddrPort, requestErr := extractAddrPortFromHost(requestHost)
	localAddrPort, localErr := extractAddrPortFromHost(localURL.Host)
	if requestErr == nil && localErr == nil {
		return requestAddrPort == localAddrPort
	}
	return strings.EqualFold(requestHost, localURL.Host) ||
		strings.EqualFold(requestHost, strings.TrimSuffix(localURL.Host, ":80"))
}

func extractAddrPortFromHost(hostAndPort string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(hostAndPort)
	if err != nil {
		host = strings.Trim(hostAndPort, "[]")
		port = "80"
	}
	return netip.ParseAddrPort(net.JoinHostPort(host, port))
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
