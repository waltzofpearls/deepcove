package cli

import (
	"path/filepath"
	"testing"
)

func TestUnderPrefix(t *testing.T) {
	cases := []struct {
		name   string
		exe    string
		prefix string
		want   bool
	}{
		{"homebrew bin", "/opt/homebrew/bin/deepcove", "/opt/homebrew", true},
		{"homebrew cellar", "/opt/homebrew/Cellar/deepcove/0.1.0/bin/deepcove", "/opt/homebrew", true},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/deepcove", "/home/linuxbrew/.linuxbrew", true},
		{"elsewhere", "/usr/local/bin/deepcove", "/opt/homebrew", false},
		{"shares a name only", "/opt/homebrew-mine/bin/deepcove", "/opt/homebrew", false},
		{"no prefix", "/usr/local/bin/deepcove", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, prefix := filepath.FromSlash(tc.exe), filepath.FromSlash(tc.prefix)
			if got := underPrefix(exe, prefix); got != tc.want {
				t.Errorf("underPrefix(%q, %q) = %v, want %v", exe, prefix, got, tc.want)
			}
		})
	}
}

func TestCellarFormula(t *testing.T) {
	cases := []struct {
		name   string
		exe    string
		prefix string
		want   string
	}{
		{"cellar", "/opt/homebrew/Cellar/deepcove/0.1.0/bin/deepcove", "/opt/homebrew", "deepcove"},
		{"formula named otherwise", "/opt/homebrew/Cellar/deepcove-cli/0.1.0/bin/deepcove", "/opt/homebrew", "deepcove-cli"},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Cellar/deepcove/0.1.0/bin/deepcove", "/home/linuxbrew/.linuxbrew", "deepcove"},
		{"under the prefix but not the cellar", "/opt/homebrew/bin/deepcove", "/opt/homebrew", ""},
		{"cellar root", "/opt/homebrew/Cellar/deepcove", "/opt/homebrew", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, prefix := filepath.FromSlash(tc.exe), filepath.FromSlash(tc.prefix)
			if got := cellarFormula(exe, prefix); got != tc.want {
				t.Errorf("cellarFormula(%q, %q) = %q, want %q", exe, prefix, got, tc.want)
			}
		})
	}
}
