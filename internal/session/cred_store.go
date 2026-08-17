package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// credentialSettingPrefix namespaces credential bundles in the settings table.
const credentialSettingPrefix = "agent-credentials/"

// CredentialStore keeps agent logins for the VM runtimes, which cannot share the named volume the
// Docker runtime uses (two running VMs cannot mount one disk image read write without corrupting it).
// A guest fetches its bundle at boot and hands back a refreshed one at shutdown.
//
// Credentials live as an opaque JSON value inside deepcove's own DuckDB file, never as a file at a
// credential shaped path. That is deliberate: an MDM scanner sweeping for `~/.claude` style paths finds
// a database, not a login. Encrypting the value at rest, with a key from the OS keystore, is the obvious
// next step and would additionally defeat content scanning.
//
// It lives here rather than in the credbroker package because the guest init imports credbroker and is
// built with CGO disabled; pulling database/sql and DuckDB in there would break that build.
type CredentialStore struct {
	db *sql.DB
}

// NewCredentialStore wires a store to the node's database. A nil db yields a nil store, which the caller
// treats as "no credential brokering", so a node without persistence still runs sessions.
func NewCredentialStore(db *sql.DB) *CredentialStore {
	if db == nil {
		return nil
	}
	return &CredentialStore{db: db}
}

// Load returns the stored bundle for an agent. Nothing stored is not an error: it means the agent has
// never logged in, and the session should start so the user can do that.
func (s *CredentialStore) Load(agent string) (map[string][]byte, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, credentialKey(agent)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: load credentials for %q: %w", agent, err)
	}
	var files map[string][]byte
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, fmt.Errorf("session: decode credentials for %q: %w", agent, err)
	}
	return files, nil
}

// Save replaces the stored bundle for an agent, keeping the one it replaces.
//
// The previous copy is the way back from a push that turns out to be worthless: a guest that lost its
// login once wiped every session's login this way.
func (s *CredentialStore) Save(agent string, files map[string][]byte) error {
	if previous, err := s.Load(agent); err == nil && len(previous) > 0 {
		if err := s.write(previousKey(agent), previous); err != nil {
			return err
		}
	}
	return s.write(credentialKey(agent), files)
}

func (s *CredentialStore) write(key string, files map[string][]byte) error {
	body, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("session: encode credentials for %q: %w", key, err)
	}
	_, err = s.db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, string(body))
	if err != nil {
		return fmt.Errorf("session: save credentials for %q: %w", key, err)
	}
	return nil
}

func credentialKey(agent string) string { return credentialSettingPrefix + agent }

// previousKey names the bundle a save replaced.
func previousKey(agent string) string { return credentialSettingPrefix + agent + "/previous" }
