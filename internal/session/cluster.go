package session

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"unicode"
)

// DefaultCluster is the cluster a node belongs to when none is set. A zero-config install therefore has
// exactly one (unnamed) cluster, and clustering is purely additive/opt-in (SPEC §5.12).
const DefaultCluster = "default"

// MaxClusterNameLen bounds a cluster label; enforced by NormalizeCluster.
const MaxClusterNameLen = 64

// reservedClusterNames are web-UI sentinels ("All clusters" filter, "unknown" marker); they fold to default.
var reservedClusterNames = map[string]bool{"all": true, "unknown": true}

// ClusterHeader is the response header a node stamps on GET /api/sessions/local to report its own
// cluster to a fan-out caller, so the aggregator can tag that node's sessions with it without changing
// the bare-array body shape (which the CLI also consumes).
const ClusterHeader = "X-Deepcove-Cluster"

// ClusterStore holds this node's cluster name: a self-declared label grouping it with the user's other
// nodes. It is concurrency-safe and mutable so the settings UI can change it live, and it persists the
// change to the settings table when a db is wired. An empty name always reads back as DefaultCluster.
type ClusterStore struct {
	mu   sync.RWMutex
	name string
	db   *sql.DB // when non-nil, Set persists the change
}

// NewClusterStore returns a store seeded with name (or DefaultCluster if empty). db is used to persist
// later Set calls; pass nil in tests that don't care about persistence.
func NewClusterStore(db *sql.DB, name string) *ClusterStore {
	return &ClusterStore{db: db, name: NormalizeCluster(name)}
}

// Name returns the current cluster name.
func (c *ClusterStore) Name() string {
	if c == nil {
		return DefaultCluster
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.name
}

// Set normalizes and persists the cluster name. It persists before the in-memory update, so a failed
// write leaves Name() consistent with what's on disk.
func (c *ClusterStore) Set(name string) error {
	if c == nil {
		return nil
	}
	name = NormalizeCluster(name)
	if c.db != nil {
		if err := SaveCluster(c.db, name); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.name = name
	c.mu.Unlock()
	return nil
}

// NormalizeCluster canonicalizes a label from any source: trim, strip control chars, cap length, and fold
// an empty or reserved-sentinel name to DefaultCluster.
func NormalizeCluster(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if r := []rune(name); len(r) > MaxClusterNameLen {
		name = strings.TrimSpace(string(r[:MaxClusterNameLen]))
	}
	if name == "" || reservedClusterNames[strings.ToLower(name)] {
		return DefaultCluster
	}
	return name
}

// LoadCluster reads the persisted cluster name from the settings table, or "" if none is stored.
func LoadCluster(db *sql.DB) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = 'cluster'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SaveCluster upserts the cluster name into the settings table.
func SaveCluster(db *sql.DB, name string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key, value) VALUES ('cluster', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		name,
	)
	return err
}
