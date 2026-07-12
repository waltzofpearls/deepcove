package connect

import "database/sql"

// loadNodeClusters reads every persisted peer cluster observation, keyed by stable node id.
func loadNodeClusters(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT stable_node_id, cluster FROM node_clusters`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, cluster string
		if err := rows.Scan(&id, &cluster); err != nil {
			return nil, err
		}
		out[id] = cluster
	}
	return out, rows.Err()
}

// saveNodeCluster upserts a peer's observed cluster (last write wins).
func saveNodeCluster(db *sql.DB, stableNodeID, cluster string) error {
	_, err := db.Exec(
		`INSERT INTO node_clusters (stable_node_id, cluster) VALUES (?, ?)
		 ON CONFLICT (stable_node_id) DO UPDATE SET cluster = excluded.cluster`,
		stableNodeID, cluster,
	)
	return err
}
