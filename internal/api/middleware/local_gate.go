// Package middleware holds the per-front-door HTTP gates for the deepcove API.
// In `deepcove start` the same mux is served on three listeners, each wrapped
// with the gate matching its trust model:
//
//   - Unix socket: ungated; filesystem permissions are the boundary (CLI).
//   - Local TCP: LocalGate here (same-machine browser).
//   - Tailnet: owner-gated via WhoIs (in package connect).
package middleware

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// LocalTokenCookie is the cookie carrying the per-run local access token.
const LocalTokenCookie = "deepcove_token"

// LocalGate restricts the local TCP front door (127.0.0.1) to same-machine
// browser access. Two independent checks, both required for the runtime API:
//
//   - Host allowlist: the request Host must be loopback (localhost / 127.x / ::1) or an explicitly
//     configured host. This defeats DNS rebinding, where a malicious site points its name at
//     127.0.0.1: its requests still carry Host: evil.example, which we reject.
//   - Token cookie: runtime /api/* (and the attach WebSocket) require a cookie whose value matches
//     the per-run token. Only the SPA served by this listener receives that cookie (seeded below on
//     non-API responses), and SameSite=Strict keeps cross-site requests from sending it. So a
//     drive-by web page (or any other local process that never loaded the SPA) can't reach the
//     runtime API even though the port is open.
//
// Non-API requests (SPA bundle, static assets, /api/health, future
// /api/setup/*) are ungated and seed the cookie. The Unix socket (CLI) and the
// owner-gated tailnet never use this gate.
func LocalGate(next http.Handler, token string, allowedHosts []string) http.Handler {
	allow := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		if host != "" {
			allow[strings.ToLower(host)] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, allow) {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		switch {
		case isRuntimeAPI(r.URL.Path):
			if !validToken(r, token) {
				http.Error(w, "unauthorized: missing or invalid local token", http.StatusUnauthorized)
				return
			}
		case !strings.HasPrefix(r.URL.Path, "/api/") && !validToken(r, token):
			// Seed the token on SPA/asset responses (any non-/api path) so the browser carries it on
			// subsequent runtime calls. /api/health and /api/setup/* are exempt from both the check and
			// seeding. HttpOnly so an XSS can't read it; SameSite=Strict so cross-site requests don't
			// send it; no Secure, the local listener is plain http on loopback.
			http.SetCookie(w, &http.Cookie{
				Name:     LocalTokenCookie,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
		}
		next.ServeHTTP(w, r)
	})
}

func validToken(r *http.Request, token string) bool {
	cookie, err := r.Cookie(LocalTokenCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
}

// isRuntimeAPI reports whether a path is a gated runtime endpoint. Everything
// under /api/ is gated except the unauthenticated health check and the (future)
// first-run setup wizard, which must work before the SPA holds a token.
func isRuntimeAPI(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	if path == "/api/health" || strings.HasPrefix(path, "/api/setup/") {
		return false
	}
	return true
}

// hostAllowed checks the request Host (sans port) against loopback and the
// configured allowlist.
func hostAllowed(requestHost string, allow map[string]struct{}) bool {
	host := requestHost
	if hostOnly, _, err := net.SplitHostPort(requestHost); err == nil {
		host = hostOnly
	}
	host = strings.ToLower(strings.Trim(host, "[]")) // strip IPv6 brackets if present
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	_, ok := allow[host]
	return ok
}
