package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/session"
)

func TestVMInstallerOffered(t *testing.T) {
	cases := []struct {
		missing string
		want    bool
	}{
		{missing: "", want: true},
		{missing: session.MissingKernel, want: true},
		{missing: session.MissingHostBinaries, want: true},
		{missing: session.MissingKVM, want: false},
		{missing: session.MissingGuestInit, want: false},
		{missing: session.MissingStartFailed, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.missing, func(t *testing.T) {
			if got := vmInstallerOffered(tc.missing); got != tc.want {
				t.Errorf("vmInstallerOffered(%q) = %v, want %v", tc.missing, got, tc.want)
			}
		})
	}
}

func TestBuiltInSourceCheckout(t *testing.T) {
	cases := []struct {
		name  string
		goMod string // "" means no go.mod next to the program
		want  bool
	}{
		{"next to deepcove's go.mod", "module github.com/waltzofpearls/deepcove\n\ngo 1.26\n", true},
		{"next to another project's go.mod", "module example.com/other\n", false},
		{"an installed copy with no go.mod", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.goMod != "" {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.goMod), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := builtInSourceCheckout(filepath.Join(dir, "deepcove")); got != tc.want {
				t.Errorf("builtInSourceCheckout = %v, want %v", got, tc.want)
			}
		})
	}
}
