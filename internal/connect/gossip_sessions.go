package connect

import (
	"context"
	"slices"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/session"
)

// FindSessionHost returns the node hosting a session, or false if no node claims it.
func (m *Manager) FindSessionHost(ctx context.Context, sessionID string) (session.NodeRef, bool) {
	if sessionID == "" {
		return session.NodeRef{}, false
	}
	for nodeID, ids := range m.GossipValues(ctx, session.GossipTopicSessionHosts) {
		if !slices.Contains(strings.Split(ids, ","), sessionID) {
			continue
		}
		if ref, ok := m.nodeRefByID(nodeID); ok {
			return ref, true
		}
	}
	return session.NodeRef{}, false
}

// nodeRefByID finds a node by its stable id, self included.
func (m *Manager) nodeRefByID(stableNodeID string) (session.NodeRef, bool) {
	for _, n := range m.Nodes() {
		if n.StableNodeID == stableNodeID {
			return toNodeRef(n), true
		}
	}
	return session.NodeRef{}, false
}
