package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	dist := t.TempDir()
	if err := build(filepath.Join("..", ".."), dist); err != nil {
		t.Fatalf("build: %v", err)
	}
	cases := []struct {
		file string
		want string
	}{
		{file: "index.html", want: "curl -fsSL https://deepcove.dev/install.sh | sh"},
		{file: "404.html", want: "Page not found"},
		{file: "install.sh", want: `main "$@"`},
		{file: "install.ps1", want: "Main"},
		{file: "_headers", want: "/install.ps1"},
		{file: "assets/style.css", want: "--primary"},
		{file: "assets/favicon.svg", want: "<svg"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dist, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Errorf("%s does not contain %q", tc.file, tc.want)
			}
		})
	}
}
