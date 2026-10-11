package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// SharedLoginStore keeps shared logins and login settings in the settings table. It lives here so the
// guest init can import sharedlogin without the database.
type SharedLoginStore struct{ db *sql.DB }

// NewSharedLoginStore keeps shared logins in db.
func NewSharedLoginStore(db *sql.DB) *SharedLoginStore { return &SharedLoginStore{db: db} }

// Load returns the shared login for agent, and false when there is none yet.
func (s *SharedLoginStore) Load(agent string) (sharedlogin.Login, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, sharedLoginKey(agent)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sharedlogin.Login{}, false, nil
	}
	if err != nil {
		return sharedlogin.Login{}, false, fmt.Errorf("session: load the shared %s login: %w", agent, err)
	}
	var login sharedlogin.Login
	if err := json.Unmarshal([]byte(raw), &login); err != nil {
		return sharedlogin.Login{}, false, fmt.Errorf("session: read the shared %s login: %w", agent, err)
	}
	return login, true, nil
}

// Save replaces the shared login for agent.
func (s *SharedLoginStore) Save(agent string, login sharedlogin.Login) error {
	return s.saveSetting(sharedLoginKey(agent), login)
}

// LoadOtherAccounts returns what the host knows of other accounts, empty when none.
func (s *SharedLoginStore) LoadOtherAccounts(agent string) (sharedlogin.OtherAccounts, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, otherAccountsKey(agent)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sharedlogin.OtherAccounts{}, nil
	}
	if err != nil {
		return sharedlogin.OtherAccounts{}, fmt.Errorf("session: load other %s accounts: %w", agent, err)
	}
	var others sharedlogin.OtherAccounts
	if err := json.Unmarshal([]byte(raw), &others); err != nil {
		return sharedlogin.OtherAccounts{}, fmt.Errorf("session: read other %s accounts: %w", agent, err)
	}
	return others, nil
}

// SaveOtherAccounts replaces what the host knows of other accounts.
func (s *SharedLoginStore) SaveOtherAccounts(agent string, others sharedlogin.OtherAccounts) error {
	return s.saveSetting(otherAccountsKey(agent), others)
}

func (s *SharedLoginStore) saveSetting(key string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, string(body))
	return err
}

// DefaultLoginMode is how new sessions log in when the user does not pick. Shared until changed.
func (s *SharedLoginStore) DefaultLoginMode(agent string) (LoginMode, error) {
	mode := LoginShared
	if _, err := s.loadSetting(defaultLoginModeKey(agent), &mode); err != nil {
		return "", fmt.Errorf("session: load the %s login default: %w", agent, err)
	}
	return mode, nil
}

// SetDefaultLoginMode records how new sessions log in.
func (s *SharedLoginStore) SetDefaultLoginMode(agent string, mode LoginMode) error {
	return s.saveSetting(defaultLoginModeKey(agent), mode)
}

// APIKey returns the user's own API key for agent, empty when none is saved.
func (s *SharedLoginStore) APIKey(agent string) (string, error) {
	var key string
	if _, err := s.loadSetting(apiKeyKey(agent), &key); err != nil {
		return "", fmt.Errorf("session: load the %s API key: %w", agent, err)
	}
	return key, nil
}

// SetAPIKey saves the user's own API key for agent. Empty removes it.
func (s *SharedLoginStore) SetAPIKey(agent, key string) error {
	if key == "" {
		_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, apiKeyKey(agent))
		return err
	}
	return s.saveSetting(apiKeyKey(agent), key)
}

func (s *SharedLoginStore) loadSetting(key string, into any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), into)
}

func sharedLoginKey(agent string) string { return "shared-login/" + agent }

func otherAccountsKey(agent string) string { return "shared-login-others/" + agent }

func defaultLoginModeKey(agent string) string { return "login-mode-default/" + agent }

func apiKeyKey(agent string) string { return "api-key/" + agent }
