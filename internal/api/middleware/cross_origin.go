package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

// crossOriginAPI reports whether a page from another origin is calling the API. Our own pages are the
// only browser callers, so any other origin is refused. The standard library's check lets every GET
// through, and a GET is how the terminal's WebSocket opens.
func crossOriginAPI(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
		// An older browser sends only Origin. The CLI and node to node calls send neither.
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		parsed, err := url.Parse(origin)
		return err != nil || !strings.EqualFold(parsed.Host, r.Host)
	default:
		return true
	}
}
