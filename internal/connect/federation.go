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

// NodeClusters returns each of the user's session nodes' cluster labels, keyed by stable node id. Self
// is read locally; online peers are queried for their /api/cluster in parallel with a short timeout.
// Used only by the Nodes page, so it fans out on demand rather than on every sessions poll.
func (m *Manager) NodeClusters(ctx context.Context) map[string]string {
	out := map[string]string{}
	nodes := m.Nodes()
	for _, n := range nodes {
		if n.Self {
			if n.StableNodeID == "" {
				continue // lone rung-1 node has no stable id yet
			}
			c := m.clusterStore.Name()
			out[n.StableNodeID] = c
			m.cacheCluster(n.StableNodeID, c)
		}
	}
	m.nodeMu.Lock()
	client := m.httpClient
	m.nodeMu.Unlock()
	if client != nil {
		type result struct{ id, cluster string }
		ch := make(chan result)
		pending := 0
		for _, node := range nodes {
			if node.Self || !node.Online || node.DNSName == "" {
				continue
			}
			pending++
			go func(id, dns string) {
				ch <- result{id, fetchPeerCluster(ctx, client, dns)}
			}(node.StableNodeID, node.DNSName)
		}
		for i := 0; i < pending; i++ {
			if r := <-ch; r.cluster != "" {
				out[r.id] = r.cluster
				m.cacheCluster(r.id, r.cluster)
			}
		}
	}
	// fill unreached nodes from the last-known cache (live values above win)
	for _, n := range nodes {
		if _, ok := out[n.StableNodeID]; !ok {
			if c := m.cachedCluster(n.StableNodeID); c != "" {
				out[n.StableNodeID] = c
			}
		}
	}
	return out
}

// fetchPeerCluster GETs a peer's /api/cluster, returning "" on any failure.
func fetchPeerCluster(ctx context.Context, client *http.Client, dnsName string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dnsName+"/api/cluster", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Cluster string `json:"cluster"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return ""
	}
	if out.Cluster == "" {
		return "" // empty stays empty, don't fabricate default
	}
	return session.NormalizeCluster(out.Cluster)
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
