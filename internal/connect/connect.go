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
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// Status is the /api/connect payload driving the wizard.
type Status struct {
	Phase    string            `json:"phase"`
	LoginURL string            `json:"login_url,omitempty"`
	URL      string            `json:"url,omitempty"` // http or https, name or IP
	TLS      bool              `json:"tls"`           // currently serving https
	MagicDNS bool              `json:"magicdns_enabled"`
	HTTPS    bool              `json:"https_enabled"`           // certs available -> upgradable
	Tailnet  string            `json:"tailnet_name,omitempty"`  // the tailnet this node is on
	Name     string            `json:"name,omitempty"`          // this node's MagicDNS name
	IPs      []string          `json:"ips,omitempty"`           // this node's tailnet IPs (v4, v6)
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
	handler     http.Handler // app mux, served (owner-gated) on the tailnet listeners

	clusterStore *session.ClusterStore // this node's cluster label, reported to fan-out callers

	clusterCacheMu sync.RWMutex      // guards clusterCache
	clusterCache   map[string]string // last-known peer cluster by stable node id; backed by the node_clusters table

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
}

func NewManager(ctx context.Context, dataDir string, db *sql.DB, handler http.Handler, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		ctx:         ctx,
		db:          db,
		logger:      logger,
		stateDir:    filepath.Join(dataDir, "tailnet"),
		enabledFile: filepath.Join(dataDir, "tailnet.enabled"),
		hostname:    defaultHostname(),
		handler:     handler,
	}
	if db != nil {
		if owner, err := loadOwner(db); err != nil {
			logger.Warn("connect: load persisted node owner", "err", err)
		} else if owner != nil {
			m.persistedOwner = owner
			m.ownerSaved = true
		}
		// seed the last-known cluster cache so offline peers survive a restart
		if cached, err := loadNodeClusters(db); err != nil {
			logger.Warn("connect: load persisted node clusters", "err", err)
		} else {
			m.clusterCache = cached
		}
	}
	return m
}

// SetClusterStore wires this node's cluster label so Self() and fan-out report it. Optional; a nil
// store reads back as the default cluster.
func (m *Manager) SetClusterStore(cs *session.ClusterStore) { m.clusterStore = cs }

// cacheCluster records a peer's observed cluster label, in memory and written through to DuckDB so it
// survives a restart. Empty ids/labels and unchanged values are ignored.
func (m *Manager) cacheCluster(stableNodeID, cluster string) {
	if stableNodeID == "" || cluster == "" {
		return
	}
	m.clusterCacheMu.Lock()
	defer m.clusterCacheMu.Unlock()
	if m.clusterCache == nil {
		m.clusterCache = make(map[string]string)
	}
	if m.clusterCache[stableNodeID] == cluster {
		return
	}
	m.clusterCache[stableNodeID] = cluster

	// persist under the lock so the DB row order matches the map order
	if m.db != nil {
		_ = saveNodeCluster(m.db, stableNodeID, cluster)
	}
}

// cachedCluster returns a peer's last-known cluster label, or "" if none was ever observed.
func (m *Manager) cachedCluster(stableNodeID string) string {
	m.clusterCacheMu.RLock()
	defer m.clusterCacheMu.RUnlock()
	return m.clusterCache[stableNodeID]
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
	if owner == nil {
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

// Connect starts the node (idempotent) and the serve loop. With no auth key this
// triggers interactive login; the URL surfaces via Status while needs_login.
func (m *Manager) Connect() (Status, error) {
	m.nodeMu.Lock()
	if m.tsNode == nil {
		tsNode := tailnet.New(m.stateDir, m.hostname)
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
		// forget drops the remembered peer clusters too (fresh identity, possibly different peers)
		m.clusterCacheMu.Lock()
		m.clusterCache = nil
		m.clusterCacheMu.Unlock()
		if m.db != nil {
			if _, err := m.db.Exec(`DELETE FROM node_clusters`); err != nil {
				m.logger.Warn("connect: clear node_clusters on reset", "err", err)
			}
		}
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
	out := Status{Phase: string(s.Phase), LoginURL: s.LoginURL, Owner: s.Owner}
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
	_, _ = m.Connect()
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
			go func() { _ = http.Serve(ln, m.ownerGate(tsNode)) }()
		}
	}
	if !m.serving443 && tsNode.Reachability().HTTPSAvailable {
		if ln, err := tsNode.ListenTLS(); err == nil {
			m.serving443 = true
			go func() { _ = http.Serve(ln, m.ownerGate(tsNode)) }()
		}
	}
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
		owner := m.owner(tsNode)
		if owner == nil || id.UserID != owner.UserID {
			http.Error(w, "forbidden: not the tsNode owner", http.StatusForbidden)
			return
		}
		m.handler.ServeHTTP(w, r)
	})
}

func (m *Manager) AttachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/connect", m.handleConnect)
	mux.HandleFunc("GET /api/connect/status", m.handleStatus)
	mux.HandleFunc("POST /api/connect/disconnect", m.handleDisconnect)
	mux.HandleFunc("POST /api/connect/reset", m.handleReset)
	mux.HandleFunc("GET /api/nodes", m.handleNodes)
	mux.HandleFunc("GET /api/nodes/web", m.handleWebNodes)
	mux.HandleFunc("GET /api/nodes/clusters", m.handleNodeClusters)
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
	writeJSON(w, http.StatusOK, m.Nodes())
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

func (m *Manager) handleNodeClusters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.NodeClusters(r.Context()))
}

func (m *Manager) handleConnect(w http.ResponseWriter, r *http.Request) {
	st, err := m.Connect()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
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
