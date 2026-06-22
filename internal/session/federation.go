package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// peerFanoutTimeout bounds each peer fan-out call so one slow node can't stall the aggregated list.
const peerFanoutTimeout = 2 * time.Second

// ErrNodeNotFound means a create targeted a NodeID that isn't one of the user's known nodes.
var ErrNodeNotFound = errors.New("session: target node not found")

// NodeRef identifies a deepcove node for host-tagging sessions and for fan-out dialing.
type NodeRef struct {
	StableNodeID string `json:"stable_node_id"`
	Hostname     string `json:"hostname"`
	DNSName      string `json:"dns_name,omitempty"`
}

// Federation supplies the cross-node view the aggregated list needs. The connect manager implements
// it (it owns the tsnet node). A nil Federation means single-node: only this machine's sessions.
type Federation interface {
	// Self is this node's identity, used to tag locally-hosted sessions.
	Self() NodeRef
	// Peers are the user's other deepcove nodes to fan out to.
	Peers() []NodeRef
	// FetchSessions GETs a peer's local sessions over the tailnet.
	FetchSessions(ctx context.Context, peer NodeRef, includeAll bool) ([]*Session, error)
	// CreateSession proxies a create to a peer over the tailnet; the session is hosted there.
	CreateSession(ctx context.Context, peer NodeRef, req CreateRequest) (*Session, error)
}

// SetFederation wires the cross-node source. Optional: without it, ListAggregated returns only this
// node's sessions (rung 1, or any single-machine setup).
func (s *Service) SetFederation(f Federation) { s.federation = f }

// SessionListResponse is the aggregated GET /api/sessions payload: sessions across all your nodes,
// each tagged with the node hosting it, plus any peers whose deepcove API didn't answer in time.
type SessionListResponse struct {
	Sessions    []*Session `json:"sessions"`
	Self        *NodeRef   `json:"self,omitempty"`
	Unreachable []NodeRef  `json:"unreachable,omitempty"`
}

// ListAggregated returns this node's sessions plus, when federated, every peer's, fanned out in
// parallel with a short per-peer timeout. Peers that don't answer are reported in Unreachable rather
// than silently dropped. Nothing is replicated: this is a query-time merge of independent per-node
// stores.
func (s *Service) ListAggregated(ctx context.Context, filter ListFilter) (*SessionListResponse, error) {
	local, err := s.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	resp := &SessionListResponse{Sessions: []*Session{}}

	if s.federation == nil {
		resp.Sessions = append(resp.Sessions, local...)
		return resp, nil
	}

	self := s.federation.Self()
	resp.Self = &self
	tagHost(local, self)
	resp.Sessions = append(resp.Sessions, local...)

	peers := s.federation.Peers()
	type fanoutResult struct {
		peer     NodeRef
		sessions []*Session
		ok       bool
	}
	results := make(chan fanoutResult, len(peers))
	for _, peer := range peers {
		go func(peer NodeRef) {
			peerCtx, cancel := context.WithTimeout(ctx, peerFanoutTimeout)
			defer cancel()
			remote, err := s.federation.FetchSessions(peerCtx, peer, filter.IncludeAll)
			if err != nil {
				results <- fanoutResult{peer: peer, ok: false}
				return
			}
			tagHost(remote, peer)
			results <- fanoutResult{peer: peer, sessions: remote, ok: true}
		}(peer)
	}
	for range peers {
		r := <-results
		if r.ok {
			resp.Sessions = append(resp.Sessions, r.sessions...)
		} else {
			resp.Unreachable = append(resp.Unreachable, r.peer)
		}
	}

	// The per-node lists are each last-activity-desc; the merge isn't, so re-sort for a coherent
	// dashboard. Unreachable peers stay in discovery order.
	sort.SliceStable(resp.Sessions, func(i, j int) bool {
		return resp.Sessions[i].LastActivityAt.After(resp.Sessions[j].LastActivityAt)
	})
	return resp, nil
}

// CreateRouted creates a session on the node the request targets. Empty NodeID, no federation, or
// this node's own id all create here; another node's id proxies the create to that peer over the
// tailnet (the session is hosted on whichever node creates it). An unknown id is ErrNodeNotFound.
func (s *Service) CreateRouted(ctx context.Context, req CreateRequest) (*Session, error) {
	if req.NodeID == "" || s.federation == nil {
		return s.Create(ctx, req)
	}
	if req.NodeID == s.federation.Self().StableNodeID {
		return s.Create(ctx, req)
	}
	for _, peer := range s.federation.Peers() {
		if peer.StableNodeID == req.NodeID {
			return s.federation.CreateSession(ctx, peer, req)
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrNodeNotFound, req.NodeID)
}

// tagHost stamps every session with the node hosting it. A fresh copy of host per session keeps the
// pointer from being shared across the slice.
func tagHost(sessions []*Session, host NodeRef) {
	for _, sess := range sessions {
		h := host
		sess.Host = &h
	}
}
