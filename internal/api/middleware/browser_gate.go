package middleware

import "net/http"

// BrowserGate stops a web page on another site from using this listener.
//
// The Host header must name this node. That is what defeats DNS rebinding: a site that points its own
// name at this node's address still sends its own name, and we reject it. Cross-origin requests are
// then rejected by the standard library's CSRF protection, which reads Sec-Fetch-Site and falls back
// to comparing Origin with Host.
//
// A request with no browser headers passes this gate, which is right for the CLI and for node to node
// calls; the owner check is what stops a stranger.
//
// hosts is read per request, because a node learns its names after it connects and can be renamed while
// it runs. An empty list refuses everything, since a node that cannot name itself cannot tell its own
// traffic from anyone else's.
func BrowserGate(next http.Handler, hosts func() []string) http.Handler {
	crossOrigin := http.NewCrossOriginProtection().Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostIsOurs(r.Host, hosts()) {
			http.Error(w, "forbidden: unexpected Host header", http.StatusForbidden)
			return
		}
		crossOrigin.ServeHTTP(w, r)
	})
}

// hostIsOurs reports whether the request Host names this node. A list, not a map: it holds a handful of
// names and is rebuilt whenever they change.
func hostIsOurs(requestHost string, ours []string) bool {
	host := normalizeHost(requestHost)
	for _, name := range ours {
		if name != "" && host == normalizeHost(name) {
			return true
		}
	}
	return false
}
