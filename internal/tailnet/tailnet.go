// Package tailnet embeds an in-process Tailscale node (tsnet) so deepcove is
// reachable on the user's tailnet. It joins via interactive browser login (no
// auth key), serves the app over the tailnet (HTTP :80, upgrading to HTTPS :443
// when the tailnet has MagicDNS + HTTPS certs), and resolves per-request identity
// via WhoIs. Used only on the Tailscale rungs; rung 1 (local) never touches it.
package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

// HostnamePrefix is the discovery marker every deepcove node's MagicDNS hostname carries. Peer
// enumeration uses it to tell deepcove peers apart from other machines on the tailnet.
const HostnamePrefix = "deepcove-"

// wasmClientOS is the OS a tailscale-web browser node reports (it's compiled GOOS=js). The in-browser
// WASM client carries the deepcove- hostname prefix too, but it's a CLIENT that never serves the API,
// so it must be excluded from hosting-node discovery.
const wasmClientOS = "js"

type Phase string

const (
	PhaseStopped    Phase = "stopped"
	PhaseStarting   Phase = "starting"
	PhaseNeedsLogin Phase = "needs_login"
	PhaseRunning    Phase = "running"
)

// Identity is a resolved Tailscale user (a request's, or the node owner's).
type Identity struct {
	UserID        tailcfg.UserID `json:"user_id"`
	LoginName     string         `json:"login_name"`
	DisplayName   string         `json:"display_name"`
	ProfilePicURL string         `json:"profile_pic_url,omitempty"`
	// Every tagged device in a tailnet reports the same user, so this identity names a machine.
	FromTaggedDevice bool `json:"from_tagged_device,omitempty"`
}

// State is the bus-driven connection state. The reachable URL is NOT here; it depends on live
// tailnet settings (MagicDNS/HTTPS). See Reachability.
type State struct {
	Phase    Phase     `json:"phase"`
	LoginURL string    `json:"login_url,omitempty"`
	Owner    *Identity `json:"owner,omitempty"`
}

// NodeInfo describes one of the user's deepcove nodes on the tailnet (this machine, or a peer).
type NodeInfo struct {
	StableNodeID string `json:"stable_node_id"`
	Hostname     string `json:"hostname"`
	DNSName      string `json:"dns_name"`
	OS           string `json:"os,omitempty"`
	Online       bool   `json:"online"`
	Self         bool   `json:"self"`
}

// Reachability is queried live, because it can change while running (e.g. the
// user enables HTTPS in the admin and we then upgrade to :443).
type Reachability struct {
	MagicDNSEnabled bool
	HTTPSAvailable  bool // MagicDNS on AND HTTPS certs enabled -> ListenTLS works
	Name            string
	TailnetName     string // the tailnet this node is on (CurrentTailnet.Name), for "you must be on X"
	IPs             []string
}

// Node wraps a tsnet.Server. hostname must already carry the deepcove- discovery
// prefix; New does not enforce it.
type Node struct {
	srv         *tsnet.Server
	localClient *local.Client
	ctx         context.Context

	mu           sync.Mutex
	state        State
	stableNodeID string
}

func New(stateDir, hostname string) *Node {
	srv := &tsnet.Server{Dir: stateDir, Hostname: hostname}
	// Route tsnet's own logging through a filter. Its default Logf is log.Printf, which reprints the
	// "To start this tsnet server, restart with TS_AUTHKEY set, or go to: <url>" reminder every few
	// seconds for the whole time a node sits unauthenticated. We already surface the login URL and
	// connection state through the IPN bus and the UI, so that line is pure noise; drop just it and
	// pass everything else through. UserLogf gets the same treatment in case the line rides that channel.
	srv.Logf = filteredLogf
	srv.UserLogf = filteredLogf
	return &Node{
		srv:   srv,
		state: State{Phase: PhaseStopped},
	}
}

// UseAuthKey enrols this node with a Tailscale auth key instead of an interactive sign-in. Call it
// before Start; afterwards the node has already chosen how to authenticate.
//
// tsnet falls back to the TS_AUTHKEY and TS_AUTH_KEY environment variables on its own, so this is for
// a key that arrives some other way, such as over the local socket.
func (n *Node) UseAuthKey(key string) { n.srv.AuthKey = key }

// noisyLogFragments are tsnet/netstack log lines deepcove suppresses because they are high-volume and
// not actionable for us:
//   - "To start this tsnet server ...": the auth reminder, reprinted every few seconds while a node is
//     unauthenticated. We already surface the login URL and state through the IPN bus and the UI.
//   - "decrementing connsInFlightByClient ... the packet was not handled": netstack's per-packet
//     in-flight bookkeeping. It fires for trailing/late packets after a flow is torn down, which our
//     frequent cross-node polling (sessions, previews, connect-status) generates in bulk. Benign for
//     app correctness; Tailscale's own rate-limit on this line is unreliable, hence the flood.
var noisyLogFragments = []string{
	"To start this tsnet server",
	"decrementing connsInFlightByClient",
}

// Tailscale marks its own verbose lines with a [v1] or [v2] prefix, which its uploader parses into a
// level and gates on. A plain Logf like ours just receives the prefix as text, so we gate on it here.
// The third form is the JSON-tagged level, "[v\x00JSON]" followed by the digit.
// Errors are never tagged, so dropping these loses nothing but per-packet and per-peer detail.
var verboseLogMarkers = []string{"[v1]", "[v2]", "[v\x00JSON]"}

// verbose is set once at startup by the CLI, and read from tsnet's own goroutines.
var verbose atomic.Bool

// SetVerbose passes tailscale's verbose lines through. Off unless the node is running at debug level.
func SetVerbose(on bool) { verbose.Store(on) }

func filteredLogf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, frag := range noisyLogFragments {
		if strings.Contains(msg, frag) {
			return
		}
	}
	if !verbose.Load() {
		for _, marker := range verboseLogMarkers {
			if strings.Contains(msg, marker) {
				return
			}
		}
	}
	log.Print(msg)
}

// Start brings up the node; with no auth key it triggers interactive login. It returns immediately;
// the login URL and running state arrive via the IPN bus and surface through State.
func (n *Node) Start(ctx context.Context) error {
	n.ctx = ctx
	n.setPhase(PhaseStarting)
	if err := n.srv.Start(); err != nil {
		return fmt.Errorf("tailnet: start: %w", err)
	}
	localClient, err := n.srv.LocalClient()
	if err != nil {
		return fmt.Errorf("tailnet: local client: %w", err)
	}
	configureLocalClient(localClient)
	n.localClient = localClient
	go n.watch(ctx)
	return nil
}

// configureLocalClient prepares the tsnet in-process LocalClient for use.
//
// OmitAuth is load-bearing: the in-process localapi is an in-memory pipe the handler serves with
// PermitRead/Write, so it needs no auth token. Without OmitAuth, every WhoIs/Status call runs
// safesocket.LocalTCPPortAndToken, which on macOS forks an external subprocess to read the
// host-tailscaled same-user proof, so the owner gate would fork once per tailnet request and peg a
// core under polling. tsnet builds its client with Dial set but forgets OmitAuth.
func configureLocalClient(localClient *local.Client) {
	localClient.OmitAuth = true
}

func (n *Node) watch(ctx context.Context) {
	w, err := n.localClient.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return
	}
	defer func() { _ = w.Close() }()
	for {
		notify, err := w.Next()
		if err != nil {
			return
		}
		if notify.BrowseToURL != nil {
			n.mu.Lock()
			n.state.Phase = PhaseNeedsLogin
			n.state.LoginURL = *notify.BrowseToURL
			n.mu.Unlock()
		}
		if notify.State != nil {
			switch *notify.State {
			case ipn.NeedsLogin:
				n.setPhase(PhaseNeedsLogin)
			case ipn.Running:
				n.onRunning()
			}
		}
	}
}

func (n *Node) onRunning() {
	status, err := n.localClient.Status(n.ctx)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state.Phase = PhaseRunning
	n.state.LoginURL = ""
	if err == nil && status.Self != nil {
		owner := &Identity{UserID: status.Self.UserID, FromTaggedDevice: status.Self.IsTagged()}
		if up, ok := status.User[status.Self.UserID]; ok {
			owner.LoginName = up.LoginName
			owner.DisplayName = up.DisplayName
			owner.ProfilePicURL = up.ProfilePicURL
		}
		n.state.Owner = owner
		n.stableNodeID = string(status.Self.ID)
	}
}

// StableNodeID is Tailscale's durable per-node id (survives logout/relogin),
// known once Running.
func (n *Node) StableNodeID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stableNodeID
}

func (n *Node) State() State {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

// Owner is the node owner (Status().Self.UserID), known once Running.
func (n *Node) Owner() *Identity {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Owner
}

// Reachability queries live status: whether :443 is usable, and the host
// name/IPs to build the URL with.
func (n *Node) Reachability() Reachability {
	if n.localClient == nil {
		return Reachability{}
	}
	status, err := n.localClient.Status(n.ctx)
	if err != nil || status.Self == nil {
		return Reachability{}
	}
	r := Reachability{Name: strings.TrimSuffix(status.Self.DNSName, ".")}
	if status.CurrentTailnet != nil {
		r.MagicDNSEnabled = status.CurrentTailnet.MagicDNSEnabled
		r.TailnetName = status.CurrentTailnet.Name
	}
	r.HTTPSAvailable = r.MagicDNSEnabled && len(status.CertDomains) > 0
	for _, ip := range status.TailscaleIPs {
		r.IPs = append(r.IPs, ip.String())
	}
	return r
}

// Nodes returns the user's deepcove nodes: this machine plus every same-user tailnet peer whose
// hostname carries the deepcove- prefix. Returns nil when status is unavailable (not yet Running).
func (n *Node) Nodes() []NodeInfo {
	if n.localClient == nil {
		return nil
	}
	status, err := n.localClient.Status(n.ctx)
	if err != nil {
		return nil
	}
	return filterDeepcoveNodes(status)
}

// filterDeepcoveNodes is the pure core of Nodes: from a tailnet status it returns this node (Self,
// always included and listed first) plus every peer that both belongs to the same Tailscale user and
// carries the deepcove- hostname prefix. Peers follow Self, sorted by hostname for stable output.
func filterDeepcoveNodes(status *ipnstate.Status) []NodeInfo {
	if status == nil || status.Self == nil {
		return nil
	}
	selfUser := status.Self.UserID
	var peers []NodeInfo
	for _, peer := range status.Peer {
		if peer.UserID != selfUser || !strings.HasPrefix(peer.HostName, HostnamePrefix) {
			continue
		}
		if peer.OS == wasmClientOS {
			continue // the browser WASM client, not a hosting node
		}
		peers = append(peers, peerToNodeInfo(peer, false))
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Hostname < peers[j].Hostname })
	return append([]NodeInfo{peerToNodeInfo(status.Self, true)}, peers...)
}

func peerToNodeInfo(peer *ipnstate.PeerStatus, self bool) NodeInfo {
	return NodeInfo{
		StableNodeID: string(peer.ID),
		Hostname:     peer.HostName,
		DNSName:      strings.TrimSuffix(peer.DNSName, "."),
		OS:           peer.OS,
		Online:       self || peer.Online, // self is, by definition, reachable from itself
		Self:         self,
	}
}

// WebNodes returns the user's in-browser web nodes on the tailnet: same-user, deepcove-prefixed peers
// that report the wasm client OS and are currently online. These host nothing, so they are deliberately
// kept out of Nodes() (and thus out of session fan-out); they exist only to be listed on the Nodes page.
func (n *Node) WebNodes() []NodeInfo {
	if n.localClient == nil {
		return nil
	}
	status, err := n.localClient.Status(n.ctx)
	if err != nil {
		return nil
	}
	return filterWebNodes(status)
}

// filterWebNodes is the pure core of WebNodes: same-user, deepcove-prefixed, os==js, ONLINE peers,
// sorted by hostname. Self is never a web node (a hosting node runs a real OS), so it is excluded.
// Web nodes are ephemeral and churn, so offline ones are dropped rather than left as stale rows.
func filterWebNodes(status *ipnstate.Status) []NodeInfo {
	if status == nil || status.Self == nil {
		return nil
	}
	selfUser := status.Self.UserID
	var webs []NodeInfo
	for _, peer := range status.Peer {
		if peer.UserID != selfUser || !strings.HasPrefix(peer.HostName, HostnamePrefix) {
			continue
		}
		if peer.OS != wasmClientOS || !peer.Online {
			continue
		}
		webs = append(webs, peerToNodeInfo(peer, false))
	}
	sort.Slice(webs, func(i, j int) bool { return webs[i].Hostname < webs[j].Hostname })
	return webs
}

// HTTPClient returns an http.Client that dials over the tailnet, used to fan out API calls to peer
// deepcove nodes (e.g. GET http://<peer-magicdns>/api/sessions/local). The peer authenticates the
// caller via WhoIs; same-user requests pass its owner gate.
func (n *Node) HTTPClient() *http.Client {
	return n.srv.HTTPClient()
}

// Listen returns the plain-HTTP tailnet listener (:80). Tailnet traffic is
// WireGuard-encrypted regardless, and the browser WASM client reaches DERP over
// wss:// fine from an http:// page (verified), so :80 is a first-class transport.
func (n *Node) Listen() (net.Listener, error) {
	return n.srv.Listen("tcp", ":80")
}

// ListenTLS returns the HTTPS listener (:443). Only call when
// Reachability().HTTPSAvailable; it provisions a MagicDNS cert on first handshake.
func (n *Node) ListenTLS() (net.Listener, error) {
	return n.srv.ListenTLS("tcp", ":443")
}

// WhoIs resolves the Tailscale user behind a request's RemoteAddr (IP or IP:port).
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (*Identity, error) {
	who, err := n.localClient.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	if who.UserProfile == nil {
		return nil, fmt.Errorf("tailnet: no user profile for %q", remoteAddr)
	}
	return &Identity{
		UserID:           who.UserProfile.ID,
		LoginName:        who.UserProfile.LoginName,
		DisplayName:      who.UserProfile.DisplayName,
		ProfilePicURL:    who.UserProfile.ProfilePicURL,
		FromTaggedDevice: who.Node != nil && who.Node.IsTagged(),
	}, nil
}

// StartLogin asks for a fresh sign-in on a node that is already up, which is how a key close to expiry
// gets extended. The URL arrives on the bus like any other login, so callers read it from State.
//
// This is what `tailscale up --force-reauth` does underneath.
func (n *Node) StartLogin(ctx context.Context) error {
	if n.localClient == nil {
		return errors.New("tailnet: this node is not running")
	}
	return n.localClient.StartLoginInteractive(ctx)
}

// Logout deregisters the node from the tailnet (removing the device + freeing
// the hostname), so a fresh Start requires a new interactive login.
func (n *Node) Logout(ctx context.Context) error {
	if n.localClient == nil {
		return nil
	}
	return n.localClient.Logout(ctx)
}

func (n *Node) Close() error {
	return n.srv.Close()
}

func (n *Node) setPhase(p Phase) {
	n.mu.Lock()
	n.state.Phase = p
	n.mu.Unlock()
}
