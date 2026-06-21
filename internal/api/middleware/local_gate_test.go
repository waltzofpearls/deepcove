package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalGate(t *testing.T) {
	const token = "secret-token"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	gate := LocalGate(next, token, []string{"deepcove.example"})

	cases := []struct {
		name          string
		method        string
		path          string
		host          string
		cookie        string // "" = none
		wantStatus    int
		wantSetCookie bool
	}{
		{"runtime, no token -> 401", "GET", "/api/sessions", "127.0.0.1:8080", "", http.StatusUnauthorized, false},
		{"runtime, valid token -> 200", "GET", "/api/sessions", "127.0.0.1:8080", token, http.StatusOK, false},
		{"runtime, wrong token -> 401", "GET", "/api/sessions", "127.0.0.1:8080", "nope", http.StatusUnauthorized, false},
		{"health exempt", "GET", "/api/health", "localhost:8080", "", http.StatusOK, false},
		{"setup exempt", "POST", "/api/setup/init", "localhost", "", http.StatusOK, false},
		{"SPA seeds cookie", "GET", "/", "localhost:8080", "", http.StatusOK, true},
		{"SPA with cookie, no reseed", "GET", "/", "localhost:8080", token, http.StatusOK, false},
		{"ipv6 loopback host", "GET", "/api/sessions", "[::1]:8080", token, http.StatusOK, false},
		{"configured host allowed", "GET", "/api/sessions", "deepcove.example", token, http.StatusOK, false},
		{"foreign host (dns rebind) -> 403", "GET", "/api/health", "evil.example", "", http.StatusForbidden, false},
		{"foreign host, even with token -> 403", "GET", "/api/sessions", "evil.example:8080", token, http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://x"+tc.path, nil)
			r.Host = tc.host
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: LocalTokenCookie, Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			gate.ServeHTTP(w, r)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			var seeded *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == LocalTokenCookie {
					seeded = c
				}
			}
			if (seeded != nil) != tc.wantSetCookie {
				t.Fatalf("set-cookie present = %v, want %v", seeded != nil, tc.wantSetCookie)
			}
			if seeded != nil {
				if seeded.Value != token {
					t.Errorf("seeded value = %q, want token", seeded.Value)
				}
				if !seeded.HttpOnly {
					t.Errorf("seeded cookie should be HttpOnly")
				}
				if seeded.SameSite != http.SameSiteStrictMode {
					t.Errorf("seeded SameSite = %v, want Strict", seeded.SameSite)
				}
			}
		})
	}
}
