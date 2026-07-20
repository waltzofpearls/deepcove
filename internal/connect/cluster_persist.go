package connect

import (
	"database/sql"
	"time"
)

// observedCluster is a peer's last known cluster plus when it was observed, so gossip can keep the
// value with the freshest timestamp when nodes disagree.
type observedCluster struct {
	cluster    string
	observedAt time.Time
}

// loadNodeClusters reads every persisted peer cluster observation, keyed by stable node id.
func loadNodeClusters(db *sql.DB) (map[string]observedCluster, error) {
	rows, err := db.Query(`SELECT stable_node_id, cluster, observed_at FROM node_clusters`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]observedCluster{}
	for rows.Next() {
		var id, cluster string
		var at sql.NullTime // pre-0006 rows have no timestamp -> zero time (oldest)
		if err := rows.Scan(&id, &cluster, &at); err != nil {
			return nil, err
		}
		out[id] = observedCluster{cluster: cluster, observedAt: at.Time}
	}
	return out, rows.Err()
}

// saveNodeCluster upserts a peer's observed cluster and timestamp (last write wins).
func saveNodeCluster(db *sql.DB, stableNodeID string, obs observedCluster) error {
	_, err := db.Exec(
		`INSERT INTO node_clusters (stable_node_id, cluster, observed_at) VALUES (?, ?, ?)
		 ON CONFLICT (stable_node_id) DO UPDATE SET cluster = excluded.cluster, observed_at = excluded.observed_at`,
		stableNodeID, obs.cluster, obs.observedAt,
	)
	return err
}
