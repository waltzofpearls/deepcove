// Package connect owns the optional Tailscale node lifecycle, the "Connect"
// wizard endpoints (rung 1 -> rung 2), and serving the app over the tailnet:
// HTTP :80 by default, upgrading live to HTTPS :443 when the tailnet has MagicDNS
// + HTTPS certs. Tailnet requests are gated to the node owner. Rung 1 (local)
// never constructs a Manager.
package connect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api/middleware"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// Status is the /api/connect payload driving the wizard.
type Status struct {
	Phase    string            `json:"phase"`
	LoginURL string            `json:"login_url,omitempty"`
	URL      string            `json:"url,omitempty"` // http or https, name or IP
	TLS      bool              `json:"tls"`           // currently serving https
	MagicDNS bool              `json:"magicdns_enabled"`
	HTTPS    bool              `json:"https_enabled"`          // certs available -> upgradable
	Tailnet  string            `json:"tailnet_name,omitempty"` // the tailnet this node is on
	Name     string            `json:"name,omitempty"`         // this node's MagicDNS name
	IPs      []string          `json:"ips,omitempty"`          // this node's tailnet IPs (v4, v6)
	Owner    *tailnet.Identity `json:"owner,omitempty"`
}

// Manager owns the tailnet node and serves the app handler over it.
type Manager struct {
	ctx         context.Context
	db          *sql.DB
	logger      *slog.Logger
	stateDir    string
	enabledFile string
	hostname    string
	version     string       // the deepcove release this node runs, gossiped so other nodes can show it
	handler     http.Handler // app mux, served (owner-gated) on the tailnet listeners

	gossipTopicsMu sync.RWMutex
	gossipTopics   map[string]func() (value string, answered bool) // how to read this node's own value, per topic

	hostnamesMu sync.RWMutex
	hostnames   []string // the names this node answers to, refreshed by the serve loop

	gossipValuesMu sync.RWMutex
	gossipValues   map[string]map[string]GossipTopicValue // topic -> node -> value

	gossipMu   sync.Mutex
	lastGossip time.Time
	gossiping  bool

	nodeMu     sync.Mutex // guards tsNode + httpClient + serveOnce (the node lifecycle)
	tsNode     *tailnet.Node
	httpClient *http.Client // cached from tsNode; reused across fan-out
	serveOnce  bool

	serveMu       sync.Mutex // guards serving80 + serving443 + enabledMarked
	serving80     bool
	serving443    bool
	enabledMarked bool // the auto-start marker has been written for the current connection

	ownerMu        sync.RWMutex      // guards persistedOwner + ownerSaved (the node owner)
	persistedOwner *tailnet.Identity // node owner from the DuckDB node table
	ownerSaved     bool

	profilePicture *profilePicture
}

func NewManager(ctx context.Context, dataDir, version string, db *sql.DB, handler http.Handler, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		ctx:            ctx,
		db:             db,
		logger:         logger,
		stateDir:       filepath.Join(dataDir, "tailnet"),
		enabledFile:    filepath.Join(dataDir, "tailnet.enabled"),
		hostname:       defaultHostname(),
		version:        version,
		handler:        handler,
		profilePicture: newProfilePicture(),
	}
	m.RegisterGossipTopic(GossipTopicVersion, func() (string, bool) {
		return m.version, true
	})
	if db != nil {
		if owner, err := loadOwner(db); err != nil {
			logger.Warn("connect: load persisted node owner", "err", err)
		} else if owner != nil {
			m.persistedOwner = owner
			m.ownerSaved = true
		}
		// seed what peers last said, so an offline peer's value survives a restart
	}
	// A test Manager has no database, and neither does a node before its first run.
	if db != nil {
		if cached, err := loadGossipValues(db); err != nil {
			logger.Warn("connect: load gossip", "err", err)
		} else {
			m.gossipValues = cached
		}
	}

	return m
}

// persistOwnerOnce records the node owner in DuckDB the first time the node is
// Running (read-once). Idempotent.
func (m *Manager) persistOwnerOnce(tsNode *tailnet.Node) {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	if m.ownerSaved {
		return
	}
	owner := tsNode.Owner()
	// A tagged node has a machine as its owner, which is not something to record.
	if owner == nil || owner.UserID == 0 || owner.FromTaggedDevice {
		return
	}
	if m.db != nil {
		if err := saveOwner(m.db, tsNode.StableNodeID(), owner); err != nil {
			return // retry on the next tick
		}
	}
	m.persistedOwner = owner
	m.ownerSaved = true
}

// owner is the authoritative node owner: the persisted record if claimed, else the live status.
func (m *Manager) owner(tsNode *tailnet.Node) *tailnet.Identity {
	m.ownerMu.RLock()
	o := m.persistedOwner
	m.ownerMu.RUnlock()
	if o != nil {
		return o
	}
	return tsNode.Owner()
}

// TailnetClient dials over the tailnet, or nil when this node is not connected.
func (m *Manager) TailnetClient() *http.Client {
	m.nodeMu.Lock()
	defer m.nodeMu.Unlock()
	return m.httpClient
}

// Connect starts the node (idempotent) and the serve loop. An empty authKey means an interactive
// sign-in, whose URL surfaces via Status while needs_login. A key enrols the node with no browser.
//
// The key is used for this connect only and is never stored: once a node is enrolled, its own state
// reconnects it, so keeping the key would be a secret held for no reason.
func (m *Manager) Connect(authKey string) (Status, error) {
	m.nodeMu.Lock()
	if m.tsNode == nil {
		tsNode := tailnet.New(m.stateDir, m.hostname)
		if authKey != "" {
			tsNode.UseAuthKey(authKey)
		}
		if err := tsNode.Start(m.ctx); err != nil {
			m.nodeMu.Unlock()
			return Status{}, err
		}
		m.tsNode = tsNode
		m.httpClient = tsNode.HTTPClient() // reused across fan-out
		// NOTE: the auto-start marker is deliberately NOT written here. We write it only once the node
		// actually reaches Running (see ensureServing), so an abandoned sign-in never leaves a marker
		// that would auto-start a half-connected node, reprinting its login URL forever, on the next boot.
	}
	if !m.serveOnce {
		m.serveOnce = true
		go m.serveLoop()
	}
	m.nodeMu.Unlock()
	return m.Status(), nil
}

// forceReauth starts a fresh sign-in on a node that is already connected, so a key near expiry can be
// extended before it lapses. An expired key needs no help: the node drops to needs_login on its own and
// Connect surfaces the URL.
func (m *Manager) forceReauth(ctx context.Context) (Status, error) {
	m.nodeMu.Lock()
	tsNode := m.tsNode
	m.nodeMu.Unlock()
	if tsNode == nil {
		return Status{}, errors.New("connect: this node is not on your Tailscale network yet")
	}
	if err := tsNode.StartLogin(ctx); err != nil {
		return Status{}, err
	}
	return m.Status(), nil
}

// teardownMode distinguishes the two teardown flavors at the call site.
type teardownMode int

const (
	keepState teardownMode = iota // Disconnect: keep tsnet state for a quick reconnect
	wipeState                     // Reset: log out of the tailnet + wipe state
)

// Disconnect stops serving + the node and disables auto-start, but KEEPS the
// tsnet state so a later Connect reconnects without a new login.
func (m *Manager) Disconnect() error { return m.teardown(keepState) }

// Reset logs the node out of the tailnet (removing the device, freeing the
// hostname) and wipes its state, so a future Connect starts fresh with a login.
func (m *Manager) Reset() error { return m.teardown(wipeState) }

func (m *Manager) teardown(mode teardownMode) error {
	m.nodeMu.Lock()
	tsNode := m.tsNode
	m.tsNode = nil
	m.httpClient = nil
	m.nodeMu.Unlock()

	// Reset the serving flags so a reconnect re-creates the listeners. The long-
	// lived serveLoop keeps running and idles while node is nil; closing the node
	// below tears down the in-flight http.Serve goroutines.
	m.serveMu.Lock()
	m.serving80 = false
	m.serving443 = false
	m.enabledMarked = false
	m.serveMu.Unlock()

	m.clearEnabled()
	if tsNode != nil {
		if mode == wipeState {
			_ = tsNode.Logout(m.ctx)
		}
		_ = tsNode.Close()
	}
	if mode == wipeState {
		_ = os.RemoveAll(m.stateDir)
		// A fresh identity means possibly different peers, so forget what the old ones said.
		if m.db != nil {
			if _, err := m.db.Exec(`DELETE FROM gossip_topic_values`); err != nil {
				m.logger.Warn("connect: clear gossip on reset", "err", err)
			}
			// The recorded owner goes with the identity, so a wrong claim is undone by signing out.
			if _, err := m.db.Exec(`DELETE FROM node`); err != nil {
				m.logger.Warn("connect: clear the node owner on reset", "err", err)
			}
		}
		m.ownerMu.Lock()
		m.persistedOwner = nil
		m.ownerSaved = false
		m.ownerMu.Unlock()
	}
	return nil
}

// Status reports connection + reachability, computing the URL from live settings.
func (m *Manager) Status() Status {
	m.nodeMu.Lock()
	tsNode := m.tsNode
	m.nodeMu.Unlock()
	if tsNode == nil {
		return Status{Phase: string(tailnet.PhaseStopped)}
	}
	s := tsNode.State()
	// The recorded owner, not the tailnet identity: on a tagged node those differ, and the recorded one
	// is who the gate lets in.
	out := Status{Phase: string(s.Phase), LoginURL: s.LoginURL, Owner: m.owner(tsNode)}
	if s.Phase != tailnet.PhaseRunning {
		return out
	}
	r := tsNode.Reachability()
	out.MagicDNS = r.MagicDNSEnabled
	out.HTTPS = r.HTTPSAvailable
	out.TLS = r.HTTPSAvailable && m.serving(true)
	out.Tailnet = r.TailnetName
	out.Name = r.Name
	out.IPs = r.IPs
	host := r.Name
	if !r.MagicDNSEnabled && len(r.IPs) > 0 {
		host = r.IPs[0]
	}
	if host != "" {
		if r.HTTPSAvailable {
			out.URL = "https://" + host
		} else {
			out.URL = "http://" + host
		}
	}
	return out
}

// markEnabled records that this node has successfully connected, so it auto-starts on the next boot.
func (m *Manager) markEnabled() error { return os.WriteFile(m.enabledFile, []byte("1\n"), 0o600) }

// clearEnabled removes the auto-start marker (on disconnect/reset, or when a stale marker is healed).
func (m *Manager) clearEnabled() { _ = os.Remove(m.enabledFile) }

// isEnabled reports whether the auto-start marker is present.
func (m *Manager) isEnabled() bool {
	_, err := os.Stat(m.enabledFile)
	return err == nil
}

// AutoStartIfEnabled re-connects on boot when the user previously connected. The marker is only written
// once a connection actually reached Running (see ensureServing), so it's a reliable "was connected"
// signal: a never-finished sign-in leaves no marker. We deliberately do NOT tear a node back down if it
// comes up needing login, because that would also kill a node the user is in the middle of signing into.
// A node that auto-starts but can't reconnect (e.g. removed from the admin) just sits at needs_login,
// which is harmless and quiet (its login reminder is filtered); the user can sign in or forget it.
func (m *Manager) AutoStartIfEnabled() {
	if !m.isEnabled() {
		return
	}
	// Empty auth key: an enrolled node reconnects from its own stored state, and an unenrolled one
	// needs a person, which a boot-time reconnect cannot supply.
	_, _ = m.Connect("")
}

// serveLoop brings up :80 once Running and upgrades to :443 the moment HTTPS becomes available,
// with no restart. It polls every 2s (the Status() call is in-process and cheap): the cert domains
// that gate :443 lag the Running state by a few seconds after a (re)connect, so a tight poll keeps
// the window where :443 isn't listening yet small enough that clients don't trip over it.
func (m *Manager) serveLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	m.ensureServing()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.ensureServing()
		}
	}
}

func (m *Manager) ensureServing() {
	m.nodeMu.Lock()
	tsNode := m.tsNode
	m.nodeMu.Unlock()
	if tsNode == nil || tsNode.State().Phase != tailnet.PhaseRunning {
		return
	}
	m.persistOwnerOnce(tsNode)
	m.serveMu.Lock()
	defer m.serveMu.Unlock()
	// Both listeners refuse a Host that is not ours, so there is nothing to serve until this node knows
	// its own names. The serve loop tries again in two seconds.
	hostnames := nodeHostnames(tsNode)
	if len(hostnames) == 0 {
		return
	}
	m.setHostnames(hostnames)
	if !m.enabledMarked {
		// Now that we've actually reached Running, persist the auto-start marker (write-once per
		// connection); this is the only place it's written.
		if err := m.markEnabled(); err == nil {
			m.enabledMarked = true
		}
	}
	if !m.serving80 {
		if ln, err := tsNode.Listen(); err == nil {
			m.serving80 = true
			go func() { _ = http.Serve(ln, m.gatedHandler(tsNode)) }()
		}
	}
	if !m.serving443 && tsNode.Reachability().HTTPSAvailable {
		if ln, err := tsNode.ListenTLS(); err == nil {
			m.serving443 = true
			go func() { _ = http.Serve(ln, m.gatedHandler(tsNode)) }()
		}
	}
}

// gatedHandler is the app behind both gates, which is what the tailnet listeners serve.
func (m *Manager) gatedHandler(tsNode *tailnet.Node) http.Handler {
	return middleware.BrowserGate(m.ownerGate(tsNode), m.currentHostnames)
}

// currentHostnames is what the gate checks a request's Host against.
func (m *Manager) currentHostnames() []string {
	m.hostnamesMu.RLock()
	defer m.hostnamesMu.RUnlock()
	return m.hostnames
}

func (m *Manager) setHostnames(names []string) {
	m.hostnamesMu.Lock()
	m.hostnames = names
	m.hostnamesMu.Unlock()
}

// nodeHostnames are the names this node answers to: its tailnet name, the short form a search domain
// completes, and its addresses.
func nodeHostnames(tsNode *tailnet.Node) []string {
	reach := tsNode.Reachability()
	names := append([]string{}, reach.IPs...)
	if reach.Name != "" {
		names = append(names, reach.Name)
		if short, _, ok := strings.Cut(reach.Name, "."); ok {
			names = append(names, short)
		}
	}
	return names
}

func (m *Manager) serving(tls bool) bool {
	m.serveMu.Lock()
	defer m.serveMu.Unlock()
	if tls {
		return m.serving443
	}
	return m.serving80
}

// ownerGate restricts tailnet access to the node owner: WhoIs the request, and
// allow only if it's the owner (Status().Self.UserID). Per-user sharing is later.
func (m *Manager) ownerGate(tsNode *tailnet.Node) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := tsNode.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden: not a tailnet peer", http.StatusForbidden)
			return
		}
		owner := m.ownerOrClaim(tsNode.StableNodeID(), m.owner(tsNode), id)
		if !callerIsOwner(id, owner) {
			http.Error(w, "forbidden: not the tsNode owner", http.StatusForbidden)
			return
		}
		m.handler.ServeHTTP(w, withCaller(r, id))
	})
}

// callerIsOwner reports whether the caller is the person this node belongs to.
func callerIsOwner(caller, owner *tailnet.Identity) bool {
	return isPerson(caller) && isPerson(owner) && caller.UserID == owner.UserID
}

// isPerson reports whether an identity names a human. A tag replaces the user on a device, and every
// tagged device in a tailnet reports the same one, so a tagged identity names a machine.
func isPerson(id *tailnet.Identity) bool {
	return id != nil && id.UserID != 0 && !id.FromTaggedDevice
}

// ownerOrClaim is who this node belongs to, claiming the caller when it belongs to nobody.
//
// A node enrolled with a tagged key has no user behind it, so enrolment recorded no one. The first
// person to reach it becomes the owner, written once. The identity comes from Tailscale rather than
// from the request, and everyone after that is compared against it.
func (m *Manager) ownerOrClaim(stableNodeID string, owner, caller *tailnet.Identity) *tailnet.Identity {
	if isPerson(owner) {
		return owner
	}
	if !isPerson(caller) || m.db == nil {
		return nil
	}
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	if isPerson(m.persistedOwner) {
		return m.persistedOwner
	}
	if err := saveOwner(m.db, stableNodeID, caller); err != nil {
		m.logger.Warn("connect: could not record the node owner", "err", err)
		return nil
	}
	// Read it back rather than trusting the write: the insert does nothing when a row already exists,
	// and that row is the owner, not this caller.
	stored, err := loadOwner(m.db)
	if err != nil || !isPerson(stored) {
		return nil
	}
	m.persistedOwner = stored
	m.ownerSaved = true
	m.logger.Info("this node is tagged, so the first person to reach it now owns it",
		"owner", stored.LoginName)
	return stored
}

func (m *Manager) AttachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/connect", m.handleConnect)
	mux.HandleFunc("GET /api/connect/status", m.handleStatus)
	mux.HandleFunc("GET /api/me", m.handleMe)
	mux.HandleFunc("GET /api/me/profile-picture", m.handleMyProfilePicture)
	mux.HandleFunc("GET /api/gossip/known", m.handleGossipKnown)
	mux.HandleFunc("POST /api/connect/disconnect", m.handleDisconnect)
	mux.HandleFunc("POST /api/connect/reset", m.handleReset)
	mux.HandleFunc("GET /api/nodes", m.handleNodes)
	mux.HandleFunc("GET /api/nodes/web", m.handleWebNodes)
}

// Nodes lists the user's deepcove nodes: this machine plus any same-user tailnet peers (discovered
// via the tsnet node). On rung 1 (no tailnet) or before the node is Running, it returns just this
// machine so the UI always has at least the local node to show.
func (m *Manager) Nodes() []tailnet.NodeInfo {
	m.nodeMu.Lock()
	tsNode := m.tsNode
	m.nodeMu.Unlock()
	if tsNode != nil {
		if nodes := tsNode.Nodes(); len(nodes) > 0 {
			return nodes
		}
	}
	host, _ := os.Hostname()
	return []tailnet.NodeInfo{{Hostname: host, Self: true, Online: true}}
}

func (m *Manager) handleNodes(w http.ResponseWriter, r *http.Request) {
	gossipedVersions := m.GossipValues(r.Context(), GossipTopicVersion)
	writeJSON(w, http.StatusOK, withVersions(m.Nodes(), gossipedVersions, m.version))
}

// withVersions fills in each node's version from what it gossiped. This node's comes straight from the
// build, which also covers a node that is not on the tailnet yet and has no id to look up.
func withVersions(nodes []tailnet.NodeInfo, versions map[string]string, thisNodeVersion string) []tailnet.NodeInfo {
	for index, node := range nodes {
		if node.Self {
			nodes[index].Version = thisNodeVersion
		} else {
			nodes[index].Version = versions[node.StableNodeID]
		}
	}
	return nodes
}

// WebNodes lists the user's in-browser web nodes on the tailnet (for the Nodes page). Empty until the
// node is Running. Kept separate from Nodes() so web nodes never enter session fan-out.
func (m *Manager) WebNodes() []tailnet.NodeInfo {
	m.nodeMu.Lock()
	tsNode := m.tsNode
	m.nodeMu.Unlock()
	if tsNode == nil {
		return nil
	}
	return tsNode.WebNodes()
}

func (m *Manager) handleWebNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.WebNodes())
}

func (m *Manager) handleConnect(w http.ResponseWriter, r *http.Request) {
	// The body is optional, so a plain POST still means "connect interactively".
	var body struct {
		AuthKey string `json:"auth_key,omitempty"`
		// ForceReauth signs in again on a node that is already connected. The auth key is not used here.
		ForceReauth bool `json:"force_reauth,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	var (
		st  Status
		err error
	)
	if body.ForceReauth {
		st, err = m.forceReauth(r.Context())
	} else {
		st, err = m.Connect(body.AuthKey)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (m *Manager) handleGossipKnown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.knownGossipValues())
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.Status())
}

func (m *Manager) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	_ = m.Disconnect()
	writeJSON(w, http.StatusOK, m.Status())
}

func (m *Manager) handleReset(w http.ResponseWriter, r *http.Request) {
	_ = m.Reset()
	writeJSON(w, http.StatusOK, m.Status())
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// defaultHostname builds the node's MagicDNS hostname with the deepcove-
// discovery prefix: "deepcove-<sanitized OS hostname>". The prefix is what peer
// discovery (tailnet.Nodes) matches on.
func defaultHostname() string {
	h, _ := os.Hostname()
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '.':
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "node"
	}
	return tailnet.HostnamePrefix + name
}
