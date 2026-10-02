package cli

import (
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
