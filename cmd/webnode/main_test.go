package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineScriptHashes(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string
	}{
		{"one inline script", "<head><script>window.x=1;</script></head>",
			[]string{"'sha256-g7sq2RbwbLEHSBobiM09D+ctI3iVsKqmfbqIiETF4t4='"}},
		{"a script with attributes", `<script type="module">window.x=1;</script>`,
			[]string{"'sha256-g7sq2RbwbLEHSBobiM09D+ctI3iVsKqmfbqIiETF4t4='"}},
		{"a script loaded from a file", `<script src="/app.js"></script>`, nil},
		{"no scripts", "<head></head>", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inlineScriptHashes([]byte(tc.html))
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("inlineScriptHashes = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	cases := []struct {
		name string
		// bundle is the ui/build to start from, file path to contents.
		bundle map[string]string
		// headers is web-node/_headers. Empty means one with the mark for the script hashes.
		headers  string
		wantWASM string
		wantErr  string
	}{
		{
			name: "a normal bundle",
			bundle: map[string]string{
				"index.html":                        "<html><head></head><body></body></html>",
				"_app/immutable/entry/app.js":       "console.log(1)",
				"_app/immutable/assets/main.x.wasm": "wasm",
			},
			wantWASM: "_app/immutable/assets/main.x.wasm",
		},
		{
			name: "no WASM file",
			bundle: map[string]string{
				"index.html": "<html><head></head></html>",
			},
			wantErr: "no WASM file",
		},
		{
			name: "two WASM files",
			bundle: map[string]string{
				"index.html":                   "<html><head></head></html>",
				"_app/immutable/assets/a.wasm": "wasm",
				"_app/immutable/assets/b.wasm": "wasm",
			},
			wantErr: "two WASM files",
		},
		{
			name: "a WASM file where the Worker does not serve it from",
			bundle: map[string]string{
				"index.html": "<html><head></head></html>",
				"main.wasm":  "wasm",
			},
			wantErr: "outside _app/immutable/assets/",
		},
		{
			name: "a headers file with nowhere to put the script hashes",
			bundle: map[string]string{
				"index.html":                        "<html><head></head></html>",
				"_app/immutable/assets/main.x.wasm": "wasm",
			},
			headers: "/*\n  X-Frame-Options: DENY\n",
			wantErr: "no INLINE_SCRIPT_HASHES",
		},
		{
			name: "an index.html with no head",
			bundle: map[string]string{
				"index.html": "<html><body></body></html>",
				"main.wasm":  "wasm",
			},
			wantErr: "no </head>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := t.TempDir()
			for path, body := range tc.bundle {
				full := filepath.Join(bundle, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.headers == "" {
				tc.headers = "/*\n  Content-Security-Policy: script-src 'self' INLINE_SCRIPT_HASHES\n"
			}
			headers := filepath.Join(t.TempDir(), "_headers")
			if err := os.WriteFile(headers, []byte(tc.headers), 0o644); err != nil {
				t.Fatal(err)
			}
			dist := filepath.Join(t.TempDir(), "dist")

			wasm, err := build(bundle, headers, dist)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if wasm != tc.wantWASM {
				t.Errorf("WASM %q, want %q", wasm, tc.wantWASM)
			}
			if _, err := os.Stat(filepath.Join(dist, filepath.FromSlash(wasm))); err == nil {
				t.Error("the WASM file was copied, but it has to stay out of the static files")
			}
			if _, err := os.Stat(filepath.Join(dist, "_app", "immutable", "entry", "app.js")); err != nil {
				t.Errorf("the rest of the bundle was not copied: %v", err)
			}
			indexHTML, err := os.ReadFile(filepath.Join(dist, "index.html"))
			if err != nil {
				t.Fatal(err)
			}
			writtenHeaders, err := os.ReadFile(filepath.Join(dist, "_headers"))
			if err != nil {
				t.Fatalf("the headers file was not written: %v", err)
			}
			for _, hash := range inlineScriptHashes(indexHTML) {
				if !strings.Contains(string(writtenHeaders), hash) {
					t.Errorf("the headers do not allow inline script %s, so the page would not start", hash)
				}
			}
			if strings.Contains(string(writtenHeaders), "INLINE_SCRIPT_HASHES") {
				t.Error("the mark for the script hashes was left in the headers")
			}
			if !strings.Contains(string(indexHTML), `"discover":true`) || !strings.Contains(string(indexHTML), `"transport":"wasm"`) {
				t.Errorf("index.html is not set up like the dashboard: %s", indexHTML)
			}
		})
	}
}
