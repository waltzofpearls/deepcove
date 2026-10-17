package cli

import (
	"cmp"
	"slices"
	"testing"
)

func TestRepeatedFlagValidateAddrs(t *testing.T) {
	cases := []struct {
		name    string
		goos    string
		values  []string
		wantErr bool
	}{
		{name: "an IPv4 address", values: []string{"100.95.206.36"}},
		{name: "an IPv6 address", values: []string{"fd7a:115c:a1e0::1"}},
		{name: "a CIDR", values: []string{"10.1.0.0/16"}},
		{name: "several at once", values: []string{"100.95.206.36", "10.1.0.0/16"}},
		{name: "a comma separated list", values: []string{"100.95.206.36,10.1.0.0/16"}},
		{name: "a bad entry in a list is rejected", values: []string{"100.95.206.36,nope"}, wantErr: true},
		{name: "nothing given", values: nil},
		// A hostname would look like it worked and then never match, since the guest routes by address.
		{name: "a hostname is rejected", values: []string{"vm.example.ts.net"}, wantErr: true},
		{name: "a typo is rejected", values: []string{"100.95.206"}, wantErr: true},
		{name: "a bad prefix length is rejected", values: []string{"10.0.0.0/99"}, wantErr: true},
		{name: "IPv6 is refused on macOS", goos: "darwin", values: []string{"fd7a:115c:a1e0::1"}, wantErr: true},
		{name: "an IPv6 CIDR is refused on macOS", goos: "darwin", values: []string{"fd7a::/48"}, wantErr: true},
		{name: "IPv4 is fine on macOS", goos: "darwin", values: []string{"100.95.206.36"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var flag repeatedFlag
			for _, v := range tc.values {
				if err := flag.Set(v); err != nil {
					t.Fatalf("Set(%q): %v", v, err)
				}
			}
			err := flag.validateAddrs(cmp.Or(tc.goos, "linux"))
			if tc.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRepeatedFlagCollectsEveryValue(t *testing.T) {
	cases := []struct {
		name    string
		values  []string
		want    []string
		wantErr bool
	}{
		{name: "repeated", values: []string{"a", "b", "c"}, want: []string{"a", "b", "c"}},
		{name: "comma separated", values: []string{"a,b,c"}, want: []string{"a", "b", "c"}},
		{name: "both at once", values: []string{"a,b", "c"}, want: []string{"a", "b", "c"}},
		{name: "spaces around commas", values: []string{"a , b"}, want: []string{"a", "b"}},
		{name: "blank is rejected", values: []string{"  "}, wantErr: true},
		{name: "a blank entry in a list is rejected", values: []string{"a,,b"}, wantErr: true},
		{name: "a trailing comma is rejected", values: []string{"a,"}, wantErr: true},
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
