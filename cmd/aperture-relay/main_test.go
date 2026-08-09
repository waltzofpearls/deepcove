package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestAllowedSource(t *testing.T) {
	cases := []struct {
		name    string
		allow   []string
		src     string
		wantOK  bool
		badAddr bool
	}{
		{name: "the allowed host", allow: []string{"100.75.35.244"}, src: "100.75.35.244", wantOK: true},
		{name: "a different host", allow: []string{"100.75.35.244"}, src: "100.75.35.245"},
		{name: "inside an allowed range", allow: []string{"100.64.0.0/10"}, src: "100.75.35.244", wantOK: true},
		{name: "outside an allowed range", allow: []string{"100.64.0.0/10"}, src: "10.0.0.1"},
		{name: "an allowed IPv6 host", allow: []string{"fd7a:115c:a1e0::1"}, src: "fd7a:115c:a1e0::1", wantOK: true},
		{name: "no allow list means allow any", allow: nil, src: "10.0.0.1", wantOK: true},
		// A source we cannot parse is refused rather than waved through.
		{name: "an unparseable source", allow: []string{"100.64.0.0/10"}, badAddr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var flag repeatedFlag
			for _, a := range tc.allow {
				if err := flag.Set(a); err != nil {
					t.Fatalf("Set(%q): %v", a, err)
				}
			}
			allowed, err := flag.prefixes()
			if err != nil {
				t.Fatalf("prefixes: %v", err)
			}
			src := netip.Addr{}
			if !tc.badAddr {
				src = netip.MustParseAddr(tc.src)
			}
			if got := allowedSource(src, allowed); got != tc.wantOK {
				t.Errorf("allowedSource(%v) = %v, want %v", tc.src, got, tc.wantOK)
			}
		})
	}
}

func TestSourceIP(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "100.75.35.244:54321", want: "100.75.35.244"},
		{in: "[fd7a:115c:a1e0::1]:443", want: "fd7a:115c:a1e0::1"},
		{in: "100.75.35.244", want: "100.75.35.244"},
		{in: "garbage", want: "invalid IP"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sourceIP(tc.in).String(); got != tc.want {
				t.Errorf("sourceIP(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The upstream must not learn who called the relay, because that is the whole reason it exists.
func TestProxyStripsForwardingHeaders(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	proxy := newProxy(strings.TrimPrefix(upstream.URL, "http://"))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	req.Header.Set("X-Real-Ip", "10.1.2.3")
	req.Header.Set("Forwarded", "for=10.1.2.3")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("%s reached the upstream as %q", h, v)
		}
	}
	// Everything the caller legitimately sent still has to arrive.
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	if got.URL.Path != "/v1/messages" {
		t.Errorf("path: got %q, want /v1/messages", got.URL.Path)
	}
}

func TestHandlerRefusesADisallowedSource(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "should not be reached")
	}))
	defer upstream.Close()

	allowed := []netip.Prefix{netip.MustParsePrefix("100.75.35.244/32")}
	handler := newHandler(strings.TrimPrefix(upstream.URL, "http://"), allowed)

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.RemoteAddr = "10.0.0.9:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	req.RemoteAddr = "100.75.35.244:1234"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("allowed source: got %d, want 200", rec.Code)
	}
}

func TestRunRefusesToBeAnOpenRelayByOmission(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no upstream", args: []string{"-allow-any"}, wantErr: "-upstream is required"},
		{
			name:    "no allow list and no explicit any",
			args:    []string{"-upstream", "ai.example.ts.net"},
			wantErr: "-allow",
		},
		{
			name:    "a bad allow value",
			args:    []string{"-upstream", "ai.example.ts.net", "-allow", "nope"},
			wantErr: "not an IP address or CIDR",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestRepeatedFlagAcceptsCommasAndRepeats(t *testing.T) {
	cases := []struct {
		name    string
		values  []string
		want    []string
		wantErr bool
	}{
		{name: "single", values: []string{"100.1.1.1"}, want: []string{"100.1.1.1"}},
		{name: "repeated", values: []string{"100.1.1.1", "100.2.2.2"}, want: []string{"100.1.1.1", "100.2.2.2"}},
		{name: "comma list", values: []string{"100.1.1.1,100.2.2.2/32"}, want: []string{"100.1.1.1", "100.2.2.2/32"}},
		{name: "spaces around commas", values: []string{"100.1.1.1 , 100.2.2.2"}, want: []string{"100.1.1.1", "100.2.2.2"}},
		{name: "comma list plus repeat", values: []string{"100.1.1.1,100.2.2.2", "100.3.3.3"}, want: []string{"100.1.1.1", "100.2.2.2", "100.3.3.3"}},
		{name: "empty", values: []string{""}, wantErr: true},
		{name: "empty entry in list", values: []string{"100.1.1.1,,100.2.2.2"}, wantErr: true},
		{name: "trailing comma", values: []string{"100.1.1.1,"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var flag repeatedFlag
			var err error
			for _, v := range tc.values {
				if err = flag.Set(v); err != nil {
					break
				}
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %v, want an error", []string(flag))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := []string(flag); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRepeatedFlagPrefixesFromACommaList(t *testing.T) {
	var flag repeatedFlag
	if err := flag.Set("100.1.1.1,100.2.0.0/16"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := flag.prefixes()
	if err != nil {
		t.Fatalf("prefixes: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("100.1.1.1/32"),
		netip.MustParsePrefix("100.2.0.0/16"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
