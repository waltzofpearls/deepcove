package connect

import (
	"database/sql"
	"time"
)

// loadGossipValues reads every remembered value, keyed by topic then stable node id. Persisted because
// an offline node's last word exists nowhere else.
func loadGossipValues(db *sql.DB) (map[string]map[string]GossipTopicValue, error) {
	rows, err := db.Query(`SELECT topic, stable_node_id, value, seen_at FROM gossip_topic_values`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]GossipTopicValue{}
	for rows.Next() {
		var topic, id, value string
		var seenAt time.Time
		if err := rows.Scan(&topic, &id, &value, &seenAt); err != nil {
			return nil, err
		}
		if out[topic] == nil {
			out[topic] = map[string]GossipTopicValue{}
		}
		out[topic][id] = GossipTopicValue{Value: value, SeenAt: seenAt}
	}
	return out, rows.Err()
}

// saveGossipValue upserts one node's value for one topic.
func saveGossipValue(db *sql.DB, topic, stableNodeID string, tv GossipTopicValue) error {
	_, err := db.Exec(
		`INSERT INTO gossip_topic_values (topic, stable_node_id, value, seen_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (topic, stable_node_id)
		 DO UPDATE SET value = excluded.value, seen_at = excluded.seen_at`,
		topic, stableNodeID, tv.Value, tv.SeenAt,
	)
	return err
}
