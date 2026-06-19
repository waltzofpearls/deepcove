package ui

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/config"
)

// TestServeShellInjectsTransportOverHTTP drives the real serving path end to end: a live httptest
// server backed by the actual embedded SvelteKit build. It proves the browser receives
// window.__deepcove in the page, and that the value tracks the per-listener transport setting
// (direct by default, wasm when overridden); i.e. the injection is real and configurable, not
// hardcoded.
func TestServeShellInjectsTransportOverHTTP(t *testing.T) {
	cases := []struct {
		name      string
		transport string // "" -> use the default
		want      string
	}{
		{name: "default is direct", transport: "", want: `window.__deepcove={"transport":"direct"}`},
		{name: "wasm override flows through", transport: "wasm", want: `window.__deepcove={"transport":"wasm"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			opts := []Option{WithConfig(&config.Config{IsDev: false}), WithMux(mux)}
			if tc.transport != "" {
				opts = append(opts, WithTransport(tc.transport))
			}
			if err := New(opts...).AttachRoutes(); err != nil {
				t.Fatalf("attach routes: %v", err)
			}

			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/")
			if err != nil {
				t.Fatalf("GET /: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			html := string(body)

			if resp.Header.Get("Content-Type") == "" || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
				t.Errorf("shell should be served as text/html, got %q", resp.Header.Get("Content-Type"))
			}
			if !strings.Contains(html, tc.want) {
				t.Errorf("served page missing %q.\n--- head ---\n%s", tc.want, head(html))
			}
			// The injected script must sit inside <head>, before the app bundle runs.
			if i, j := strings.Index(html, "window.__deepcove"), strings.Index(html, "</head>"); i < 0 || j < 0 || i > j {
				t.Errorf("injected script not inside <head> (script@%d, </head>@%d)", i, j)
			}
		})
	}
}

func head(html string) string {
	if i := strings.Index(html, "</head>"); i >= 0 {
		return html[:i+len("</head>")]
	}
	if len(html) > 600 {
		return html[:600]
	}
	return html
}

func TestInjectRuntimeConfig(t *testing.T) {
	cases := []struct {
		name      string
		html      string
		transport string
		target    string
		wantHas   string // substring that must appear, or "" to assert unchanged
	}{
		{
			name:      "injects before head close",
			html:      "<html><head><title>x</title></head><body></body></html>",
			transport: "direct",
			wantHas:   `<script>window.__deepcove={"transport":"direct"};</script></head>`,
		},
		{
			name:      "wasm mode carries the tailnet target",
			html:      "<head></head>",
			transport: "wasm",
			target:    "https://node.example.ts.net",
			wantHas:   `"target":"https://node.example.ts.net"`,
		},
		{
			name:      "no target key when empty",
			html:      "<head></head>",
			transport: "direct",
			wantHas:   `{"transport":"direct"}`, // no "target"
		},
		{
			name:      "no head leaves document untouched",
			html:      "<html><body>no head</body></html>",
			transport: "direct",
			wantHas:   "", // unchanged
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := injectRuntimeConfig([]byte(tc.html), tc.transport, tc.target)
			if tc.wantHas == "" {
				if !bytes.Equal(got, []byte(tc.html)) {
					t.Fatalf("expected unchanged, got %q", got)
				}
				return
			}
			if !strings.Contains(string(got), tc.wantHas) {
				t.Fatalf("missing %q in %q", tc.wantHas, got)
			}
			// The script must land inside the original head, before </head>.
			if strings.Contains(tc.html, "</head>") &&
				strings.Index(string(got), "window.__deepcove") > strings.Index(string(got), "</head>") {
				t.Fatalf("script injected after </head>: %q", got)
			}
		})
	}
}
