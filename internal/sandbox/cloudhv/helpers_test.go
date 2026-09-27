package cloudhv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPasstArgs(t *testing.T) {
	args := passtArgs("/run/passt.sock", []string{"--runas", "1000"}, []string{"127.0.0.53", "fd00::1"})
	if !slices.Contains(args, "--no-map-gw") {
		t.Errorf("args = %v, want --no-map-gw", args)
	}
	for _, flag := range []struct {
		name string
		want []string
	}{
		{name: "-a", want: []string{passtGuestAddress}},
		{name: "-n", want: []string{passtGuestPrefix}},
		{name: "-g", want: []string{passtGuestGateway}},
		{name: "-D", want: []string{passtDNSv4, passtDNSv6}},
		{name: "--dns-forward", want: []string{passtDNSv4, passtDNSv6}},
		{name: "--dns-host", want: []string{"127.0.0.53", "fd00::1"}},
	} {
		if got := valuesOf(args, flag.name); !slices.Equal(got, flag.want) {
			t.Errorf("%s = %v, want %v", flag.name, got, flag.want)
		}
	}
	if args[len(args)-1] != "1000" {
		t.Errorf("args = %v, want the runas flags kept at the end", args)
	}
}

func TestHostNameservers(t *testing.T) {
	cases := []struct {
		name string
		conf string
		want []string
	}{
		{name: "systemd-resolved", conf: "nameserver 127.0.0.53\n", want: []string{"127.0.0.53"}},
		{name: "tailscale first", conf: "nameserver 100.100.100.100\nnameserver 8.8.8.8\n", want: []string{"100.100.100.100"}},
		{name: "both families", conf: "nameserver 10.0.0.2\nnameserver fd00::1\n", want: []string{"10.0.0.2", "fd00::1"}},
		{name: "comments and junk skipped", conf: "# x\nsearch lan\nnameserver nope\nnameserver 1.1.1.1\n", want: []string{"1.1.1.1"}},
		{
			name: "addresses passt refuses are skipped",
			conf: "nameserver fe80::1%eth0\nnameserver fe80::2\nnameserver 0.0.0.0\nnameserver ::\nnameserver 9.9.9.9\n",
			want: []string{"9.9.9.9"},
		},
		{name: "nothing", conf: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "resolv.conf")
			if err := os.WriteFile(path, []byte(tc.conf), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := hostNameservers(path); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// valuesOf returns every value that follows flag in args.
func valuesOf(args []string, flag string) []string {
	var values []string
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}
