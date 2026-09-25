package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	cases := []struct {
		name string
		// bundle is the ui/build to start from, file path to contents.
		bundle   map[string]string
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
			headers := filepath.Join(t.TempDir(), "_headers")
			if err := os.WriteFile(headers, []byte("/*\n  X-Frame-Options: DENY\n"), 0o644); err != nil {
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
			if _, err := os.Stat(filepath.Join(dist, "_headers")); err != nil {
				t.Errorf("the headers file was not copied: %v", err)
			}
			index, err := os.ReadFile(filepath.Join(dist, "index.html"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(index), `"discover":true`) || !strings.Contains(string(index), `"transport":"wasm"`) {
				t.Errorf("index.html is not set up like the dashboard: %s", index)
			}
		})
	}
}
