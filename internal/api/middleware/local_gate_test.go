package middleware

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fromUser builds a request as LocalConnContext leaves it: marked as from deepcove's own user or not.
func fromUser(request *http.Request, same bool) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), sameUserKey{}, same))
}

func TestLocalGate(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	gate := LocalGate(next, []string{"deepcove.example"})
	cases := []struct {
		name       string
		path       string
		host       string
		sameUser   bool
		wantStatus int
	}{
		{"the dashboard", "/", "localhost:9000", true, http.StatusOK},
		{"the API", "/api/sessions", "127.0.0.1:9000", true, http.StatusOK},
		{"ipv6 loopback host", "/api/sessions", "[::1]:9000", true, http.StatusOK},
		{"configured host", "/api/sessions", "deepcove.example", true, http.StatusOK},
		{"another user, the dashboard", "/", "localhost:9000", false, http.StatusForbidden},
		{"another user, the API", "/api/sessions", "127.0.0.1:9000", false, http.StatusForbidden},
		{"another user, the health check", "/api/health", "127.0.0.1:9000", false, http.StatusForbidden},
		{"foreign host (dns rebind)", "/api/health", "evil.example", true, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := fromUser(httptest.NewRequest(http.MethodGet, "http://x"+tc.path, nil), tc.sameUser)
			request.Host = tc.host
			recorder := httptest.NewRecorder()
			gate.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestLocalGate_RefusesOtherOrigins(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	gate := LocalGate(next, nil)
	cases := []struct {
		name      string
		method    string
		path      string
		fetchSite string
		want      int
	}{
		{"a write from a page on another port", http.MethodPost, "/api/sessions", "same-site", http.StatusForbidden},
		{"a write from a page on another site", http.MethodPost, "/api/sessions", "cross-site", http.StatusForbidden},
		{"a terminal socket from another site", http.MethodGet, "/api/sessions/abc/attach", "cross-site", http.StatusForbidden},
		{"a write from the dashboard itself", http.MethodPost, "/api/sessions", "same-origin", http.StatusOK},
		{"a link from another site to the dashboard", http.MethodGet, "/", "cross-site", http.StatusOK},
		{"no browser headers", http.MethodPost, "/api/sessions", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := fromUser(httptest.NewRequest(tc.method, "http://x"+tc.path, nil), true)
			request.Host = "127.0.0.1:9000"
			if tc.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			recorder := httptest.NewRecorder()
			gate.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}

func TestLocalConnContext_MarksOurOwnConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	ctx := LocalConnContext(context.Background(), server)
	if same, _ := ctx.Value(sameUserKey{}).(bool); !same {
		t.Error("a connection this process made should count as its own user's")
	}
}
