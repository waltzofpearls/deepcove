package cloudhv

import (
	"net/netip"
	"slices"
	"testing"
)

func TestPastaArgs(t *testing.T) {
	guard := guardNetworkArgs([]string{"100.95.206.36"}, []netip.Prefix{netip.MustParsePrefix("2001:db8:5::/64")})
	args := pastaArgs("/bin/deepcove", guard, "/opt/passt", []string{"--socket", "/run/net.sock"})
	// Each of these carries the guest back to the host if it is left out.
	for _, flag := range []string{"--no-map-gw", "--config-net"} {
		if !slices.Contains(args, flag) {
			t.Errorf("args = %v, want %s", args, flag)
		}
	}
	for _, flag := range []struct {
		name string
		want []string
	}{
		{name: "-T", want: []string{"none"}},
		{name: "-U", want: []string{"none"}},
		{name: "-a", want: []string{pastaAddress}},
		{name: "--dns-forward", want: []string{pastaDNS}},
		{name: "--block", want: []string{"2001:db8:5::/64"}},
		{name: "--allow", want: []string{"100.95.206.36"}},
	} {
		if got := valuesOf(args, flag.name); !slices.Equal(got, flag.want) {
			t.Errorf("%s = %v, want %v", flag.name, got, flag.want)
		}
	}
	command := args[slices.Index(args, "--")+1:]
	if !slices.Equal(command[:3], []string{"/bin/deepcove", "vm", "guard-network"}) {
		t.Errorf("pasta runs %v, want deepcove's guard-network first", command)
	}
	passt := command[slices.Index(command, "--")+1:]
	if !slices.Equal(passt, []string{"/opt/passt", "--socket", "/run/net.sock"}) {
		t.Errorf("then %v, want passt with its own args", passt)
	}
}

func TestPasstArgs(t *testing.T) {
	args := passtArgs("/run/passt.sock", []string{"--runas", "1000"})
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
		{name: "--dns-host", want: []string{pastaDNS}},
	} {
		if got := valuesOf(args, flag.name); !slices.Equal(got, flag.want) {
			t.Errorf("%s = %v, want %v", flag.name, got, flag.want)
		}
	}
	if args[len(args)-1] != "1000" {
		t.Errorf("args = %v, want the runas flags kept at the end", args)
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
