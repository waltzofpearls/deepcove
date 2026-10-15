package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrossOriginAPI(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		fetchSite string
		origin    string
		want      bool
	}{
		{name: "our own page", path: "/api/sessions", fetchSite: "same-origin", want: false},
		{name: "typed into the address bar", path: "/api/health", fetchSite: "none", want: false},
		{name: "another site", path: "/api/sessions/abc/attach", fetchSite: "cross-site", want: true},
		{name: "another port", path: "/api/sessions/abc/attach", fetchSite: "same-site", want: true},
		{name: "older browser, our origin", path: "/api/sessions", origin: "http://127.0.0.1:9000", want: false},
		{name: "older browser, another origin", path: "/api/sessions", origin: "https://evil.example", want: true},
		{name: "older browser, a sandboxed frame", path: "/api/sessions", origin: "null", want: true},
		{name: "the CLI", path: "/api/sessions", want: false},
		{name: "a link to the dashboard", path: "/", fetchSite: "cross-site", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://x"+tc.path, nil)
			request.Host = "127.0.0.1:9000"
			if tc.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if got := crossOriginAPI(request); got != tc.want {
				t.Errorf("crossOriginAPI = %v, want %v", got, tc.want)
			}
		})
	}
}
