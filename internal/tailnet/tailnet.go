// Package tailnet embeds an in-process Tailscale node (tsnet) so deepcove is
// reachable on the user's tailnet. It joins via interactive browser login (no
// auth key), serves the app over the tailnet (HTTP :80, upgrading to HTTPS :443
// when the tailnet has MagicDNS + HTTPS certs), and resolves per-request identity
// via WhoIs. Used only on the Tailscale rungs; rung 1 (local) never touches it.
package tailnet

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

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
}

// State is the bus-driven connection state. The reachable URL is NOT here; it depends on live
// tailnet settings (MagicDNS/HTTPS). See Reachability.
type State struct {
	Phase    Phase     `json:"phase"`
	LoginURL string    `json:"login_url,omitempty"`
	Owner    *Identity `json:"owner,omitempty"`
}

// Reachability is queried live, because it can change while running (e.g. the
// user enables HTTPS in the admin and we then upgrade to :443).
type Reachability struct {
	MagicDNSEnabled bool
	HTTPSAvailable  bool // MagicDNS on AND HTTPS certs enabled -> ListenTLS works
	Name            string
	IPs             []string
}

// Node wraps a tsnet.Server. hostname must already carry the deepcove- discovery
// prefix; New does not enforce it.
type Node struct {
	srv *tsnet.Server
	lc  *local.Client
	ctx context.Context

	mu           sync.Mutex
	state        State
	stableNodeID string
}

func New(stateDir, hostname string) *Node {
	return &Node{
		srv:   &tsnet.Server{Dir: stateDir, Hostname: hostname},
		state: State{Phase: PhaseStopped},
	}
}

// Start brings up the node; with no auth key it triggers interactive login. It returns immediately;
// the login URL and running state arrive via the IPN bus and surface through State.
func (n *Node) Start(ctx context.Context) error {
	n.ctx = ctx
	n.setPhase(PhaseStarting)
	if err := n.srv.Start(); err != nil {
		return fmt.Errorf("tailnet: start: %w", err)
	}
	lc, err := n.srv.LocalClient()
	if err != nil {
		return fmt.Errorf("tailnet: local client: %w", err)
	}
	configureLocalClient(lc)
	n.lc = lc
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
func configureLocalClient(lc *local.Client) {
	lc.OmitAuth = true
}

func (n *Node) watch(ctx context.Context) {
	w, err := n.lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
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
	st, err := n.lc.Status(n.ctx)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state.Phase = PhaseRunning
	n.state.LoginURL = ""
	if err == nil && st.Self != nil {
		owner := &Identity{UserID: st.Self.UserID}
		if up, ok := st.User[st.Self.UserID]; ok {
			owner.LoginName = up.LoginName
			owner.DisplayName = up.DisplayName
			owner.ProfilePicURL = up.ProfilePicURL
		}
		n.state.Owner = owner
		n.stableNodeID = string(st.Self.ID)
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
	if n.lc == nil {
		return Reachability{}
	}
	st, err := n.lc.Status(n.ctx)
	if err != nil || st.Self == nil {
		return Reachability{}
	}
	r := Reachability{Name: strings.TrimSuffix(st.Self.DNSName, ".")}
	if st.CurrentTailnet != nil {
		r.MagicDNSEnabled = st.CurrentTailnet.MagicDNSEnabled
	}
	r.HTTPSAvailable = r.MagicDNSEnabled && len(st.CertDomains) > 0
	for _, ip := range st.TailscaleIPs {
		r.IPs = append(r.IPs, ip.String())
	}
	return r
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
	who, err := n.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	if who.UserProfile == nil {
		return nil, fmt.Errorf("tailnet: no user profile for %q", remoteAddr)
	}
	return &Identity{
		UserID:        who.UserProfile.ID,
		LoginName:     who.UserProfile.LoginName,
		DisplayName:   who.UserProfile.DisplayName,
		ProfilePicURL: who.UserProfile.ProfilePicURL,
	}, nil
}

// Logout deregisters the node from the tailnet (removing the device + freeing
// the hostname), so a fresh Start requires a new interactive login.
func (n *Node) Logout(ctx context.Context) error {
	if n.lc == nil {
		return nil
	}
	return n.lc.Logout(ctx)
}

func (n *Node) Close() error {
	return n.srv.Close()
}

func (n *Node) setPhase(p Phase) {
	n.mu.Lock()
	n.state.Phase = p
	n.mu.Unlock()
}
