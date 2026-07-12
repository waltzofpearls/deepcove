package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalTokenCookieName(t *testing.T) {
	// Distinct ports must give distinct cookie names, so two instances on one loopback host (where
	// cookies aren't port-isolated) don't clobber each other's token.
	a := LocalTokenCookieName("127.0.0.1:8080")
	b := LocalTokenCookieName("127.0.0.1:8081")
	if a == b {
		t.Errorf("distinct ports share a cookie name: %q", a)
	}
	if a != LocalTokenCookieBaseName+"_8080" {
		t.Errorf("cookie name = %q, want %q", a, LocalTokenCookieBaseName+"_8080")
	}
	if got := LocalTokenCookieName("noport"); got != LocalTokenCookieBaseName {
		t.Errorf("no-port addr -> %q, want base %q", got, LocalTokenCookieBaseName)
	}
}

func TestLocalGatePerPortCookie(t *testing.T) {
	const token = "secret-token"
	name := LocalTokenCookieName("127.0.0.1:8081") // deepcove_token_8081
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	gate := LocalGate(next, token, name, nil)

	do := func(path, cookieName, cookieVal string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://x"+path, nil)
		r.Host = "127.0.0.1:8081"
		if cookieName != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookieVal})
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w
	}

	if w := do("/api/sessions", name, token); w.Code != http.StatusOK {
		t.Errorf("suffixed cookie: status %d, want 200", w.Code)
	}
	if w := do("/api/sessions", LocalTokenCookieBaseName, token); w.Code != http.StatusUnauthorized {
		t.Errorf("base cookie name must not authorize the suffixed gate: status %d, want 401", w.Code)
	}
	w := do("/", "", "")
	var seeded *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			seeded = c
		}
	}
	if seeded == nil {
		t.Fatalf("expected a seeded Set-Cookie named %q", name)
	}
	if seeded.Value != token {
		t.Errorf("seeded value = %q, want token", seeded.Value)
	}
}

func TestLocalGate(t *testing.T) {
	const token = "secret-token"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	gate := LocalGate(next, token, LocalTokenCookieBaseName, []string{"deepcove.example"})

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
				r.AddCookie(&http.Cookie{Name: LocalTokenCookieBaseName, Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			gate.ServeHTTP(w, r)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			var seeded *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == LocalTokenCookieBaseName {
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
