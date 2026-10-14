//go:build linux

package cli

import (
	"net/netip"
	"slices"
	"testing"
)

func TestParseGuardNetworkArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantBlocked []netip.Prefix
		wantAllowed []string
		wantCommand []string
		wantErr     bool
	}{
		{
			name:        "blocks, allows, then passt",
			args:        []string{"--block", "2001:db8:5::1/64", "--allow", "100.95.206.36", "--", "/opt/passt", "--quiet"},
			wantBlocked: []netip.Prefix{netip.MustParsePrefix("2001:db8:5::/64")},
			wantAllowed: []string{"100.95.206.36"},
			wantCommand: []string{"/opt/passt", "--quiet"},
		},
		{name: "nothing to run", args: []string{"--block", "10.0.0.0/8"}, wantErr: true},
		{name: "not a network", args: []string{"--block", "nope", "--", "/opt/passt"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGuardNetworkArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGuardNetworkArgs: %v", err)
			}
			if !slices.Equal(got.blocked, tc.wantBlocked) || !slices.Equal(got.allowed, tc.wantAllowed) ||
				!slices.Equal(got.command, tc.wantCommand) {
				t.Errorf("got %+v, want blocked %v, allowed %v, command %v", got, tc.wantBlocked, tc.wantAllowed, tc.wantCommand)
			}
		})
	}
}
