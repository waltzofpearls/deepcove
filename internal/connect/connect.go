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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// Status is the /api/connect payload driving the wizard.
type Status struct {
	Phase    string            `json:"phase"`
	LoginURL string            `json:"login_url,omitempty"`
	URL      string            `json:"url,omitempty"` // http or https, name or IP
	TLS      bool              `json:"tls"`           // currently serving https
	MagicDNS bool              `json:"magicdns_enabled"`
	HTTPS    bool              `json:"https_enabled"` // certs available -> upgradable
	Owner    *tailnet.Identity `json:"owner,omitempty"`
}

// Manager owns the tailnet node and serves the app handler over it.
type Manager struct {
	ctx         context.Context
	db          *sql.DB
	stateDir    string
	enabledFile string
	hostname    string
	handler     http.Handler // app mux, served (owner-gated) on the tailnet listeners

	nodeMu    sync.Mutex // guards tsNode + serveOnce (the node lifecycle)
	tsNode    *tailnet.Node
	serveOnce bool

	serveMu    sync.Mutex
	serving80  bool
	serving443 bool

	ownerMu        sync.RWMutex
	persistedOwner *tailnet.Identity // node owner from the DuckDB node table
	ownerSaved     bool
}

func NewManager(ctx context.Context, dataDir string, db *sql.DB, handler http.Handler) *Manager {
	m := &Manager{
		ctx:         ctx,
		db:          db,
		stateDir:    filepath.Join(dataDir, "tailnet"),
		enabledFile: filepath.Join(dataDir, "tailnet.enabled"),
		hostname:    defaultHostname(),
		handler:     handler,
	}
	if db != nil {
		if owner, err := loadOwner(db); err == nil && owner != nil {
			m.persistedOwner = owner
			m.ownerSaved = true
		}
	}
	return m
}

// persistOwnerOnce records the node owner in DuckDB the first time the node is
// Running (SPEC §6.1, read-once). Idempotent.
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

// owner is the authoritative node owner — the persisted record if claimed, else
// the live status.
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
		_ = os.WriteFile(m.enabledFile, []byte("1\n"), 0o600)
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
	m.nodeMu.Unlock()

	// Reset the serving flags so a reconnect re-creates the listeners. The long-
	// lived serveLoop keeps running and idles while node is nil; closing the node
	// below tears down the in-flight http.Serve goroutines.
	m.serveMu.Lock()
	m.serving80 = false
	m.serving443 = false
	m.serveMu.Unlock()

	_ = os.Remove(m.enabledFile)
	if tsNode != nil {
		if mode == wipeState {
			_ = tsNode.Logout(m.ctx)
		}
		_ = tsNode.Close()
	}
	if mode == wipeState {
		_ = os.RemoveAll(m.stateDir)
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

// AutoStartIfEnabled re-connects on boot when the user previously connected.
func (m *Manager) AutoStartIfEnabled() {
	if _, err := os.Stat(m.enabledFile); err == nil {
		_, _ = m.Connect()
	}
}

// serveLoop brings up :80 once Running and upgrades to :443 the moment HTTPS
// becomes available — no restart. It polls every 2s (the Status() call is
// in-process and cheap): the cert domains that gate :443 lag the Running state
// by a few seconds after a (re)connect, so a tight poll keeps the window where
// :443 isn't listening yet small enough that clients don't trip over it.
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
// discovery prefix (SPEC §5.2): "deepcove-<sanitized OS hostname>".
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
	return "deepcove-" + name
}
