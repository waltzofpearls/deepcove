package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// Manager implements session.Federation so the session layer can aggregate sessions across the
// user's deepcove nodes. Dialing rides the tsnet node's tailnet HTTP client, and each peer
// authenticates the caller via WhoIs (same-user requests pass its owner gate).

// Self is this node, used to tag locally-hosted sessions. It carries this node's cluster label.
func (m *Manager) Self() session.NodeRef {
	for _, node := range m.Nodes() {
		if node.Self {
			ref := toNodeRef(node)
			ref.Cluster = m.clusterStore.Name()
			return ref
		}
	}
	return session.NodeRef{Cluster: m.clusterStore.Name()}
}

// ClusterObservation is a node's cluster label and when it was observed, exchanged during gossip.
type ClusterObservation struct {
	Cluster    string    `json:"cluster"`
	ObservedAt time.Time `json:"observed_at"`
}

// gossipMinInterval throttles the background gossip refresh: at most one round this often, however many
// pollers hit NodeClusters.
const gossipMinInterval = 5 * time.Second

// NodeClusters returns each session node's cluster label, keyed by stable node id. It returns the merged
// cache immediately (so a slow/dead peer never stalls the Nodes page) and refreshes it via a throttled
// background gossip round. A cold cache warms up synchronously so the first response isn't blank. Self is
// always live; the rest come from the cache, which gossip keeps consistent across nodes.
func (m *Manager) NodeClusters(ctx context.Context) map[string]string {
	if m.clusterCacheLen() == 0 {
		m.gossipOnce(ctx)
	} else {
		m.maybeGossipAsync()
	}
	return m.buildClusterMap()
}

func (m *Manager) buildClusterMap() map[string]string {
	out := map[string]string{}
	for _, n := range m.Nodes() {
		switch {
		case n.StableNodeID == "":
			continue // lone rung-1 node has no stable id yet
		case n.Self:
			out[n.StableNodeID] = m.clusterStore.Name()
		default:
			if c := m.cachedCluster(n.StableNodeID); c != "" {
				out[n.StableNodeID] = c
			}
		}
	}
	return out
}

// gossipOnce pulls every online peer's known map and merges it freshest-wins into the cache.
func (m *Manager) gossipOnce(ctx context.Context) {
	m.nodeMu.Lock()
	client := m.httpClient
	m.nodeMu.Unlock()
	if client == nil {
		return
	}
	ch := make(chan map[string]ClusterObservation)
	pending := 0
	for _, n := range m.Nodes() {
		if n.Self || !n.Online || n.DNSName == "" {
			continue
		}
		pending++
		go func(dns string) { ch <- fetchKnownClusters(ctx, client, dns) }(n.DNSName)
	}
	for i := 0; i < pending; i++ {
		for id, obs := range <-ch {
			if obs.Cluster == "" {
				continue
			}
			m.observeCluster(id, session.NormalizeCluster(obs.Cluster), obs.ObservedAt)
		}
	}
}

// maybeGossipAsync runs a gossip round in the background, unless one is already running or the last
// finished less than gossipMinInterval ago. It uses the Manager's context so it outlives the request.
func (m *Manager) maybeGossipAsync() {
	m.gossipMu.Lock()
	if m.gossiping || time.Since(m.lastGossip) < gossipMinInterval {
		m.gossipMu.Unlock()
		return
	}
	m.gossiping = true
	m.gossipMu.Unlock()
	go func() {
		m.gossipOnce(m.ctx)
		m.gossipMu.Lock()
		m.gossiping = false
		m.lastGossip = time.Now()
		m.gossipMu.Unlock()
	}()
}

// knownClusterMap is this node's memory of every peer's cluster (from the cache) plus its own cluster
// stamped now. Served over /api/nodes/clusters/known for peers to pull during gossip.
func (m *Manager) knownClusterMap() map[string]ClusterObservation {
	out := map[string]ClusterObservation{}
	for id, obs := range m.knownClusters() {
		out[id] = ClusterObservation{Cluster: obs.cluster, ObservedAt: obs.observedAt}
	}
	if self := m.Self(); self.StableNodeID != "" {
		out[self.StableNodeID] = ClusterObservation{Cluster: self.Cluster, ObservedAt: time.Now()}
	}
	return out
}

// fetchKnownClusters GETs a peer's known-cluster map, returning nil on any failure.
func fetchKnownClusters(ctx context.Context, client *http.Client, dnsName string) map[string]ClusterObservation {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dnsName+"/api/nodes/clusters/known", nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out map[string]ClusterObservation
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	return out
}

// Peers are the user's other deepcove nodes that are online. Offline nodes are skipped so fan-out
// doesn't pay a per-peer timeout (and raise a spurious "couldn't reach") for a machine that's simply
// powered off; they still appear on the Nodes page via Nodes().
func (m *Manager) Peers() []session.NodeRef {
	var peers []session.NodeRef
	for _, node := range m.Nodes() {
		if !node.Self && node.Online {
			peers = append(peers, toNodeRef(node))
		}
	}
	return peers
}

// FetchSessions GETs a peer's /api/sessions/local over the tailnet, and reads the peer's cluster label
// from the response header so the aggregator can tag that peer's sessions with it.
func (m *Manager) FetchSessions(ctx context.Context, peer session.NodeRef, includeAll bool) ([]*session.Session, string, error) {
	m.nodeMu.Lock()
	client := m.httpClient
	m.nodeMu.Unlock()
	if client == nil {
		return nil, "", fmt.Errorf("connect: not on a tailnet")
	}
	if peer.DNSName == "" {
		return nil, "", fmt.Errorf("connect: peer %q has no MagicDNS name", peer.Hostname)
	}
	url := "http://" + peer.DNSName + "/api/sessions/local"
	if includeAll {
		url += "?all=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("connect: peer %q returned status %d", peer.Hostname, resp.StatusCode)
	}
	var out []*session.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("connect: decode peer %q sessions: %w", peer.Hostname, err)
	}
	// normalize the peer-reported label (a misbehaving same-user peer shouldn't persist an odd one)
	cluster := resp.Header.Get(session.ClusterHeader)
	if cluster != "" {
		cluster = session.NormalizeCluster(cluster)
	}
	// Remember it so the Nodes page can keep showing this peer's cluster after it goes offline.
	m.cacheCluster(peer.StableNodeID, cluster)
	return out, cluster, nil
}

// CreateSession proxies a create to a peer's POST /api/sessions over the tailnet. NodeID is cleared
// so the peer creates locally rather than re-routing. The session is hosted on the peer.
func (m *Manager) CreateSession(ctx context.Context, peer session.NodeRef, req session.CreateRequest) (*session.Session, error) {
	m.nodeMu.Lock()
	client := m.httpClient
	m.nodeMu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("connect: not on a tailnet")
	}
	if peer.DNSName == "" {
		return nil, fmt.Errorf("connect: peer %q has no MagicDNS name", peer.Hostname)
	}
	req.NodeID = ""
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+peer.DNSName+"/api/sessions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("connect: peer %q create returned status %d", peer.Hostname, resp.StatusCode)
	}
	var out session.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("connect: decode peer %q create: %w", peer.Hostname, err)
	}
	return &out, nil
}

func toNodeRef(node tailnet.NodeInfo) session.NodeRef {
	return session.NodeRef{
		StableNodeID: node.StableNodeID,
		Hostname:     node.Hostname,
		DNSName:      node.DNSName,
	}
}
