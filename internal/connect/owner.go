package connect

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/waltzofpearls/deepcove/internal/tailnet"
	"tailscale.com/tailcfg"
)

// loadOwner reads the persisted node owner from the node table, or nil if the
// node hasn't been claimed yet.
func loadOwner(db *sql.DB) (*tailnet.Identity, error) {
	var uid string
	var email sql.NullString
	err := db.QueryRow(`SELECT owner_user_id, owner_email FROM node WHERE id = 1`).Scan(&uid, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n, _ := strconv.ParseInt(uid, 10, 64)
	return &tailnet.Identity{UserID: tailcfg.UserID(n), LoginName: email.String}, nil
}

// saveOwner records the node owner on first claim. ON CONFLICT DO NOTHING gives "read once, never
// changes" semantics: a later login can't silently re-own the node's data.
func saveOwner(db *sql.DB, stableNodeID string, o *tailnet.Identity) error {
	_, err := db.Exec(
		`INSERT INTO node (id, stable_node_id, owner_user_id, owner_email)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		stableNodeID, strconv.FormatInt(int64(o.UserID), 10), o.LoginName,
	)
	return err
}
