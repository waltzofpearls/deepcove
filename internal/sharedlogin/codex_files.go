package sharedlogin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// codexHostRefreshes stands in for the refresh token in a session's copy. Codex will not load a login
// without one, and reading it back as no refresh token keeps the session from offering the copy to the host.
const codexHostRefreshes = "deepcove-host-refreshes"

// CodexLoginFiles is where Codex keeps its login under a home folder.
type CodexLoginFiles struct {
	Home string
}

var _ LoginFiles = CodexLoginFiles{}

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

// TokenPath is Codex's auth.json.
func (f CodexLoginFiles) TokenPath() string { return filepath.Join(f.Home, ".codex", "auth.json") }

// Read returns Codex's current login. Codex has no first run setup to skip, so it always counts as done.
func (f CodexLoginFiles) Read() (Login, bool, error) {
	auth, err := readSettings(f.TokenPath())
	if err != nil {
		return Login{}, false, err
	}
	var tokens codexTokens
	if raw, ok := auth["tokens"]; ok {
		if err := json.Unmarshal(raw, &tokens); err != nil {
			return Login{}, false, err
		}
	}
	if tokens.AccessToken == "" {
		return Login{}, true, nil
	}
	refreshToken := tokens.RefreshToken
	if refreshToken == codexHostRefreshes {
		refreshToken = ""
	}
	login, err := codexLogin(tokens.AccessToken, refreshToken, tokens.IDToken)
	return login, true, err
}

// WriteTokens puts the login in auth.json, keeping anything else Codex keeps there. last_refresh is now,
// so Codex does not count the login as old and try to refresh it itself.
func (f CodexLoginFiles) WriteTokens(login Login) error {
	fields, err := codexFieldsOf(login)
	if err != nil {
		return err
	}
	auth, err := readSettings(f.TokenPath())
	if err != nil {
		return err
	}
	refreshToken := login.RefreshToken
	if refreshToken == "" {
		refreshToken = codexHostRefreshes
	}
	values := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": codexTokens{
			IDToken:      fields.IDToken,
			AccessToken:  login.AccessToken,
			RefreshToken: refreshToken,
			AccountID:    login.AccountID,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339Nano),
	}
	for name, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		auth[name] = encoded
	}
	if _, ok := auth["OPENAI_API_KEY"]; !ok {
		auth["OPENAI_API_KEY"] = json.RawMessage("null")
	}
	body, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	return writeFileAtomic(f.TokenPath(), body)
}

// WriteAccount does nothing: Codex reads the account from its tokens.
func (f CodexLoginFiles) WriteAccount(Login) error { return nil }

// RemoveTokens deletes auth.json, which logs Codex out.
func (f CodexLoginFiles) RemoveTokens() error {
	if err := os.Remove(f.TokenPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
