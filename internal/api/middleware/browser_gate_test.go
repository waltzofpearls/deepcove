package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserGate(t *testing.T) {
	cases := []struct {
		name      string
		hosts     []string
		host      string
		method    string
		fetchSite string
		want      int
	}{
		{name: "full name", hosts: []string{"node.tail1234.ts.net"}, host: "node.tail1234.ts.net", want: 200},
		{name: "short name", hosts: []string{"node.tail1234.ts.net"}, host: "node", want: 403},
		{name: "address with a port", hosts: []string{"100.64.0.1"}, host: "100.64.0.1:80", want: 200},
		{name: "ipv6 in brackets", hosts: []string{"fd7a::1"}, host: "[fd7a::1]", want: 200},
		{name: "another site's name", hosts: []string{"node"}, host: "evil.example", want: 403},
		{name: "no names known", host: "anything", want: 403},
		{name: "post from another site", hosts: []string{"node"}, host: "node", method: "POST",
			fetchSite: "cross-site", want: 403},
		{name: "post from our own page", hosts: []string{"node"}, host: "node", method: "POST",
			fetchSite: "same-origin", want: 200},
		{name: "post from the CLI", hosts: []string{"node"}, host: "node", method: "POST", want: 200},
		{name: "terminal socket from another site", hosts: []string{"node"}, host: "node", fetchSite: "cross-site",
			want: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = "GET"
			}
			ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			req := httptest.NewRequest(method, "/api/sessions", nil)
			req.Host = tc.host
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			rec := httptest.NewRecorder()
			BrowserGate(ok, func() []string { return tc.hosts }).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status: got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestBrowserGateFollowsARename(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	names := []string{"old.tail1234.ts.net"}
	gate := BrowserGate(ok, func() []string { return names })

	req := httptest.NewRequest("GET", "/api/sessions", nil)
	req.Host = "new.tail1234.ts.net"
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("before the rename: status = %d, want 403", rec.Code)
	}

	names = []string{"new.tail1234.ts.net"}
	rec = httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("after the rename: status = %d, want 200", rec.Code)
	}
}
