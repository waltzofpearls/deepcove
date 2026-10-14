//go:build darwin

package applevf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"

	"github.com/Code-Hex/vz/v3"
	"github.com/containers/gvisor-tap-vsock/pkg/services/dhcp"
	"github.com/containers/gvisor-tap-vsock/pkg/services/dns"
	"github.com/containers/gvisor-tap-vsock/pkg/services/forwarder"
	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/waltzofpearls/deepcove/internal/netguard"
)

// The VM's own network. Nothing else is on it, so the guest's private network block leaves it reachable.
const (
	userNetworkSubnet  = "192.168.127.0/24"
	userNetworkGateway = "192.168.127.1"
	userNetworkMAC     = "5a:94:ef:e4:0c:dd"
	userNetworkMTU     = 1500
	userNetworkNIC     = 1
)

// userNetwork gives the VM a network deepcove runs itself, wired the way Lima does it on macOS. Every
// connection the guest opens is made by this process, which refuses the ones netguard refuses. The guest
// cannot change that, root or not, which it can with the routes inside it.
func userNetwork(allow []string) (vz.NetworkDeviceAttachment, error) {
	allowed, err := netguard.ParseAllowList(allow)
	if err != nil {
		return nil, err
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, fmt.Errorf("applevf: network socket pair: %w", err)
	}
	for _, fd := range fds {
		// Lima's sizes: the defaults drop packets under load.
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1<<20)
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	}
	vmEnd, hostEnd := os.NewFile(uintptr(fds[0]), "vm network"), os.NewFile(uintptr(fds[1]), "host network")
	// vz keeps a copy of the descriptor, so ours closes either way.
	attachment, err := vz.NewFileHandleNetworkDeviceAttachment(vmEnd)
	_ = vmEnd.Close()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("applevf: network attachment: %w", err), hostEnd.Close())
	}
	conn, err := net.FileConn(hostEnd)
	_ = hostEnd.Close()
	if err != nil {
		return nil, fmt.Errorf("applevf: network host end: %w", err)
	}
	networkSwitch, err := startUserNetwork(allowed)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	go func() { _ = networkSwitch.Accept(context.Background(), conn, types.BessProtocol) }()
	return attachment, nil
}

// startUserNetwork is gvisor-tap-vsock's virtualnetwork.New with only what a VM needs: DHCP, DNS on the
// gateway, and outbound connections. Built from its parts so netguard can stand in front of each forwarder,
// which the library offers no hook for.
func startUserNetwork(allowed []netip.Prefix) (*tap.Switch, error) {
	configuration := &types.Configuration{
		MTU:               userNetworkMTU,
		Subnet:            userNetworkSubnet,
		GatewayIP:         userNetworkGateway,
		GatewayMacAddress: userNetworkMAC,
		Protocol:          types.BessProtocol,
	}
	ipPool, err := tap.NewIPPool(userNetworkSubnet)
	if err != nil {
		return nil, err
	}
	if err := ipPool.Reserve(userNetworkGateway, userNetworkMAC); err != nil {
		return nil, err
	}
	endpoint, err := tap.NewLinkEndpoint(false, userNetworkMTU, userNetworkMAC, userNetworkGateway, nil)
	if err != nil {
		return nil, err
	}
	networkSwitch := tap.NewSwitch(false)
	endpoint.Connect(networkSwitch)
	networkSwitch.Connect(endpoint)

	networkStack, err := userNetworkStack(endpoint)
	if err != nil {
		return nil, err
	}
	// The host's networks are read for every new connection, so one it joins later, a new Wi-Fi say, is
	// blocked straight away. A failed read refuses the connection rather than check against a short list.
	refused := func(id stack.TransportEndpointID) bool {
		addr, ok := netip.AddrFromSlice(id.LocalAddress.AsSlice())
		if !ok {
			return true
		}
		hostNetworks, err := netguard.HostNetworks()
		return err != nil || netguard.Refuses(addr, allowed, hostNetworks)
	}
	// A handler that answers false leaves the packet to the stack, which refuses it: a TCP reset, or an
	// ICMP port unreachable, so the guest fails fast instead of waiting out a timeout.
	guard := func(handle func(stack.TransportEndpointID, *stack.PacketBuffer) bool) func(stack.TransportEndpointID, *stack.PacketBuffer) bool {
		return func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
			return !refused(id) && handle(id, pkt)
		}
	}
	var natLock sync.Mutex
	nat := map[tcpip.Address]tcpip.Address{}
	tcpForwarder := forwarder.TCP(networkStack, nat, &natLock, false, 0, 0)
	udpForwarder := forwarder.UDP(networkStack, nat, &natLock, false)
	icmpForwarder := forwarder.ICMP(networkStack, nat, &natLock)
	networkStack.SetTransportProtocolHandler(tcp.ProtocolNumber, guard(tcpForwarder.HandlePacket))
	networkStack.SetTransportProtocolHandler(udp.ProtocolNumber, guard(udpForwarder.HandlePacket))
	networkStack.SetTransportProtocolHandler(icmp.ProtocolNumber4, guard(icmpForwarder.HandlePacket))

	if err := serveUserNetworkDNS(networkStack); err != nil {
		return nil, err
	}
	dhcpServer, err := dhcp.New(configuration, networkStack, ipPool)
	if err != nil {
		return nil, err
	}
	go func() { _ = dhcpServer.Serve() }()
	return networkSwitch, nil
}

func userNetworkStack(endpoint stack.LinkEndpoint) (*stack.Stack, error) {
	networkStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4},
	})
	if err := networkStack.CreateNIC(userNetworkNIC, endpoint); err != nil {
		return nil, errors.New(err.String())
	}
	gateway := netip.MustParseAddr(userNetworkGateway)
	if err := networkStack.AddProtocolAddress(userNetworkNIC, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(gateway.As4()).WithPrefix(),
	}, stack.AddressProperties{}); err != nil {
		return nil, errors.New(err.String())
	}
	// The stack answers for every address the guest sends to, which is what lets it forward them.
	networkStack.SetSpoofing(userNetworkNIC, true)
	networkStack.SetPromiscuousMode(userNetworkNIC, true)
	subnet := netip.MustParsePrefix(userNetworkSubnet)
	route, err := tcpip.NewSubnet(tcpip.AddrFrom4(subnet.Addr().As4()), tcpip.MaskFromBytes(net.CIDRMask(subnet.Bits(), 32)))
	if err != nil {
		return nil, err
	}
	networkStack.SetRouteTable([]tcpip.Route{{Destination: route, NIC: userNetworkNIC}})
	return networkStack, nil
}

// serveUserNetworkDNS answers the guest's DNS on the gateway, using this machine's own resolver. That one
// may sit on a private address the guest is refused, so the guest never asks it directly.
func serveUserNetworkDNS(networkStack *stack.Stack) error {
	gateway := tcpip.AddrFrom4(netip.MustParseAddr(userNetworkGateway).As4())
	udpConn, err := gonet.DialUDP(networkStack, &tcpip.FullAddress{NIC: userNetworkNIC, Addr: gateway, Port: 53}, nil, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	tcpListener, err := gonet.ListenTCP(networkStack, tcpip.FullAddress{NIC: userNetworkNIC, Addr: gateway, Port: 53}, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	server, err := dns.New(udpConn, tcpListener, nil)
	if err != nil {
		return err
	}
	go func() { _ = server.Serve() }()
	go func() { _ = server.ServeTCP() }()
	return nil
}
