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

func newTestLogin(t *testing.T) *LocalLogin {
	t.Helper()
	login, err := NewLocalLogin(t.TempDir(), LocalURL("127.0.0.1:9000"))
	if err != nil {
		t.Fatal(err)
	}
	return login
}

func TestLocalGate(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	login := newTestLogin(t)
	gate := LocalGate(next, []string{"deepcove.example"}, LocalURL("127.0.0.1:9000"), login)
	cases := []struct {
		name       string
		path       string
		host       string
		unlocked   bool
		wantStatus int
	}{
		{"the dashboard", "/", "127.0.0.1:9000", true, http.StatusOK},
		{"the API", "/api/sessions", "127.0.0.1:9000", true, http.StatusOK},
		{"ipv6 loopback host", "/api/sessions", "[::1]:9000", true, http.StatusOK},
		{"configured host", "/api/sessions", "deepcove.example", true, http.StatusOK},
		{"a locked browser, the dashboard", "/", "127.0.0.1:9000", false, http.StatusOK},
		{"a locked browser, the API", "/api/sessions", "127.0.0.1:9000", false, http.StatusUnauthorized},
		{"a locked browser, the health check", "/api/health", "127.0.0.1:9000", false, http.StatusUnauthorized},
		{"the dashboard on localhost", "/sessions", "localhost:9000", false, http.StatusFound},
		{"foreign host (dns rebind)", "/api/health", "evil.example", true, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := fromUser(httptest.NewRequest(http.MethodGet, "http://x"+tc.path, nil), true)
			request.Host = tc.host
			if tc.unlocked {
				request.AddCookie(&http.Cookie{
					Name:  login.cookieName,
					Value: login.cookieValue,
				})
			}
			recorder := httptest.NewRecorder()
			gate.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

// Another OS user is refused before anything else, even holding a cookie that reached them.
func TestLocalGate_RefusesOtherUsers(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	login := newTestLogin(t)
	gate := LocalGate(next, nil, LocalURL("127.0.0.1:9000"), login)
	cases := []struct {
		name     string
		path     string
		sameUser bool
		want     int
	}{
		{"this user, the API", "/api/sessions", true, http.StatusOK},
		{"another user, the API", "/api/sessions", false, http.StatusForbidden},
		{"another user, the page", "/", false, http.StatusForbidden},
		{"another user, an unlock link", UnlockPath + "?code=abc", false, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := fromUser(httptest.NewRequest(http.MethodGet, "http://x"+tc.path, nil), tc.sameUser)
			request.Host = "127.0.0.1:9000"
			request.AddCookie(&http.Cookie{
				Name:  login.cookieName,
				Value: login.cookieValue,
			})
			recorder := httptest.NewRecorder()
			gate.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}

func TestIsLocalURLHost(t *testing.T) {
	cases := []struct {
		name        string
		listenAddr  string
		requestHost string
		want        bool
	}{
		{"the same host and port", "127.0.0.1:9000", "127.0.0.1:9000", true},
		{"another port", "127.0.0.1:9000", "127.0.0.1:9001", false},
		{"localhost", "127.0.0.1:9000", "localhost:9000", false},
		{"port 80, left out by the browser", "127.0.0.1:80", "127.0.0.1", true},
		{"port 80, written out", "127.0.0.1:80", "127.0.0.1:80", true},
		{"ipv6 on port 80", "[::1]:80", "[::1]", true},
		{"ipv6 written out in full", "[0:0:0:0:0:0:0:1]:9000", "[::1]:9000", true},
		{"ipv6 written out in full on port 80", "[0:0:0:0:0:0:0:1]:80", "[::1]", true},
		{"ipv6 on another port", "[0:0:0:0:0:0:0:1]:9000", "[::1]:9001", false},
		{"no port, when deepcove is not on 80", "127.0.0.1:8080", "127.0.0.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLocalURLHost(tc.requestHost, LocalURL(tc.listenAddr)); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
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

func TestLocalGate_RefusesOtherOrigins(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	login := newTestLogin(t)
	gate := LocalGate(next, nil, LocalURL("127.0.0.1:9000"), login)
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
			request.AddCookie(&http.Cookie{
				Name:  login.cookieName,
				Value: login.cookieValue,
			})
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

func TestLocalURL(t *testing.T) {
	cases := []struct {
		listenAddr string
		want       string
	}{
		{"127.0.0.1:9000", "http://127.0.0.1:9000/"},
		{"0.0.0.0:9001", "http://127.0.0.1:9001/"},
		{":9002", "http://127.0.0.1:9002/"},
		{"[::1]:9003", "http://[::1]:9003/"},
	}
	for _, tc := range cases {
		t.Run(tc.listenAddr, func(t *testing.T) {
			if got := LocalURL(tc.listenAddr).String(); got != tc.want {
				t.Errorf("LocalURL(%q) = %q, want %q", tc.listenAddr, got, tc.want)
			}
		})
	}
}
