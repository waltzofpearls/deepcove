package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// Manager implements session.Federation so the session layer can aggregate sessions across the
// user's deepcove nodes. Dialing rides the tsnet node's tailnet HTTP client, and each peer
// authenticates the caller via WhoIs (same-user requests pass its owner gate).

// Self is this node, used to tag locally-hosted sessions.
func (m *Manager) Self() session.NodeRef {
	for _, n := range m.Nodes() {
		if n.Self {
			return toNodeRef(n)
		}
	}
	return session.NodeRef{}
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

// FetchSessions GETs a peer's /api/sessions/local over the tailnet.
func (m *Manager) FetchSessions(ctx context.Context, peer session.NodeRef, includeAll bool) ([]*session.Session, error) {
	m.nodeMu.Lock()
	client := m.httpClient
	m.nodeMu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("connect: not on a tailnet")
	}
	if peer.DNSName == "" {
		return nil, fmt.Errorf("connect: peer %q has no MagicDNS name", peer.Hostname)
	}
	url := "http://" + peer.DNSName + "/api/sessions/local"
	if includeAll {
		url += "?all=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("connect: peer %q returned status %d", peer.Hostname, resp.StatusCode)
	}
	var out []*session.Session
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("connect: decode peer %q sessions: %w", peer.Hostname, err)
	}
	return out, nil
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
