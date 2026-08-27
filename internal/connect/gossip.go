package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Nodes tell each other small facts about themselves, one topic at a time. A node is the only writer for
// its own values, so merging is just "keep the freshest".

// gossipMinInterval throttles the background refresh: one round this often at most, however many callers
// ask.
const gossipMinInterval = 5 * time.Second

// GossipTopicValue is what one node last said about one topic.
type GossipTopicValue struct {
	Value  string    `json:"value"`
	SeenAt time.Time `json:"seen_at"`
}

// RegisterGossipTopic says how to read this node's own value for a topic. Read fresh on every round, so
// a topic never has to push.
func (m *Manager) RegisterGossipTopic(topic string, current func() (value string, answered bool)) {
	m.gossipTopicsMu.Lock()
	defer m.gossipTopicsMu.Unlock()
	if m.gossipTopics == nil {
		m.gossipTopics = map[string]func() (value string, answered bool){}
	}
	m.gossipTopics[topic] = current
}

// GossipValues returns every node's value for one topic, keyed by stable node id. It answers from the
// cache so a slow peer never stalls a caller, and refreshes in the background. This node's own value is
// read live.
func (m *Manager) GossipValues(ctx context.Context, topic string) map[string]string {
	if m.gossipCacheLen() == 0 {
		m.gossipOnce(ctx)
	} else {
		m.maybeGossipAsync()
	}
	out := map[string]string{}
	m.gossipValuesMu.RLock()
	for id, tv := range m.gossipValues[topic] {
		out[id] = tv.Value
	}
	m.gossipValuesMu.RUnlock()
	if self := m.Self().StableNodeID; self != "" {
		if v, answered := m.ownGossipValue(topic); answered {
			out[self] = v
		}
	}
	return out
}

// mergeGossipValue applies what a node said, keeping the freshest. An empty value is a real answer,
// meaning that node hosts nothing; a node that cannot answer publishes nothing at all.
func (m *Manager) mergeGossipValue(topic, stableNodeID, value string, at time.Time) {
	if topic == "" || stableNodeID == "" {
		return
	}
	m.gossipValuesMu.Lock()
	if m.gossipValues == nil {
		m.gossipValues = map[string]map[string]GossipTopicValue{}
	}
	byNode := m.gossipValues[topic]
	if byNode == nil {
		byNode = map[string]GossipTopicValue{}
		m.gossipValues[topic] = byNode
	}
	// An identical value keeps its original stamp, so a value that never changes does not look newer
	// every round.
	if cur, ok := byNode[stableNodeID]; ok && (cur.Value == value || cur.SeenAt.After(at)) {
		m.gossipValuesMu.Unlock()
		return
	}
	tv := GossipTopicValue{Value: value, SeenAt: at}
	byNode[stableNodeID] = tv
	m.gossipValuesMu.Unlock()

	if m.db != nil {
		if err := saveGossipValue(m.db, topic, stableNodeID, tv); err != nil {
			m.logger.Warn("connect: save gossip value", "topic", topic, "err", err)
		}
	}
}

func (m *Manager) gossipCacheLen() int {
	m.gossipValuesMu.RLock()
	defer m.gossipValuesMu.RUnlock()
	return len(m.gossipValues)
}

// ownGossipValue reads this node's own value for a topic. False means nothing registered it, or it
// could not answer.
func (m *Manager) ownGossipValue(topic string) (value string, answered bool) {
	m.gossipTopicsMu.RLock()
	current := m.gossipTopics[topic]
	m.gossipTopicsMu.RUnlock()
	if current == nil {
		return "", false
	}
	return current()
}

// knownGossipValues is everything this node believes, plus its own topics stamped now. Serving other
// nodes' values too is what keeps a fact alive once its owner goes offline.
func (m *Manager) knownGossipValues() map[string]map[string]GossipTopicValue {
	out := map[string]map[string]GossipTopicValue{}
	m.gossipValuesMu.RLock()
	for topic, byNode := range m.gossipValues {
		known := make(map[string]GossipTopicValue, len(byNode))
		for id, tv := range byNode {
			known[id] = tv
		}
		out[topic] = known
	}
	m.gossipValuesMu.RUnlock()

	self := m.Self().StableNodeID
	if self == "" {
		return out
	}
	m.gossipTopicsMu.RLock()
	topics := make([]string, 0, len(m.gossipTopics))
	for topic := range m.gossipTopics {
		topics = append(topics, topic)
	}
	m.gossipTopicsMu.RUnlock()

	now := time.Now()
	for _, topic := range topics {
		v, answered := m.ownGossipValue(topic)
		if !answered {
			continue
		}
		if out[topic] == nil {
			out[topic] = map[string]GossipTopicValue{}
		}
		out[topic][self] = GossipTopicValue{Value: v, SeenAt: now}
	}
	return out
}

// gossipOnce pulls every online peer's known map and merges it.
func (m *Manager) gossipOnce(ctx context.Context) {
	m.nodeMu.Lock()
	client := m.httpClient
	m.nodeMu.Unlock()
	if client == nil {
		return
	}
	var dnsNames []string
	for _, n := range m.Nodes() {
		if n.Self || !n.Online || n.DNSName == "" {
			continue
		}
		dnsNames = append(dnsNames, n.DNSName)
	}
	if len(dnsNames) == 0 {
		return
	}
	known := make(chan map[string]map[string]GossipTopicValue, len(dnsNames))
	for _, dns := range dnsNames {
		go func(dns string) { known <- fetchKnownGossip(ctx, client, dns) }(dns)
	}
	for range dnsNames {
		for topic, byNode := range <-known {
			for id, tv := range byNode {
				m.mergeGossipValue(topic, id, tv.Value, tv.SeenAt)
			}
		}
	}
}

// maybeGossipAsync runs a round in the background, unless one is running or finished less than
// gossipMinInterval ago. It uses the Manager's context so it outlives the request.
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

// gossipFetchTimeout bounds one peer's answer. A peer that accepts the connection and never replies
// would otherwise park the round forever, and no later round can start while one is running.
const gossipFetchTimeout = 2 * time.Second

// fetchKnownGossip GETs a peer's known map, returning nil on any failure.
func fetchKnownGossip(
	ctx context.Context, client *http.Client, dnsName string,
) map[string]map[string]GossipTopicValue {
	ctx, cancel := context.WithTimeout(ctx, gossipFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dnsName+"/api/gossip/known", nil)
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
	var out map[string]map[string]GossipTopicValue
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	return out
}
