package sharedlogin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ClaudeLoginFiles is where Claude Code keeps its login under a home folder.
type ClaudeLoginFiles struct {
	Home string
}

var _ LoginFiles = ClaudeLoginFiles{}

// TokenPath is Claude's credentials file.
func (f ClaudeLoginFiles) TokenPath() string { return f.credentialsPath() }

func (f ClaudeLoginFiles) credentialsPath() string {
	return filepath.Join(f.Home, ".claude", ".credentials.json")
}

func (f ClaudeLoginFiles) settingsPath() string {
	return filepath.Join(f.Home, ".claude.json")
}

// Read returns Claude's current login and account, and whether its first run setup is done.
func (f ClaudeLoginFiles) Read() (Login, bool, error) {
	var credentials struct {
		OAuth ClaudeOAuth `json:"claudeAiOauth"`
	}
	if err := readJSONIfThere(f.credentialsPath(), &credentials); err != nil {
		return Login{}, false, err
	}
	settings, err := readSettings(f.settingsPath())
	if err != nil {
		return Login{}, false, err
	}
	login, err := ClaudeLogin(credentials.OAuth, settings["oauthAccount"])
	if err != nil {
		return Login{}, false, err
	}
	var onboarded bool
	_ = json.Unmarshal(settings["hasCompletedOnboarding"], &onboarded)
	return login, onboarded, nil
}

// WriteTokens replaces Claude's credentials file. A running Claude notices and loads it.
func (f ClaudeLoginFiles) WriteTokens(login Login) error {
	oauth, _, err := ClaudeFiles(login)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]ClaudeOAuth{"claudeAiOauth": oauth})
	if err != nil {
		return err
	}
	return writeFileAtomic(f.credentialsPath(), body)
}

// RemoveTokens deletes Claude's credentials file. The account in ~/.claude.json stays, so the next
// /login skips first run setup.
func (f ClaudeLoginFiles) RemoveTokens() error {
	if err := os.Remove(f.credentialsPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// WriteAccount sets the account and marks first run setup done in ~/.claude.json. The session's other
// settings there, like its MCP servers, stay.
func (f ClaudeLoginFiles) WriteAccount(login Login) error {
	_, account, err := ClaudeFiles(login)
	if err != nil {
		return err
	}
	settings, err := readSettings(f.settingsPath())
	if err != nil {
		return err
	}
	settings["hasCompletedOnboarding"] = json.RawMessage("true")
	if len(account) > 0 {
		settings["oauthAccount"] = account
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return writeFileAtomic(f.settingsPath(), body)
}

// A missing or broken ~/.claude.json reads as empty, which is what Claude does too.
func readSettings(path string) (map[string]json.RawMessage, error) {
	settings := map[string]json.RawMessage{}
	if err := readJSONIfThere(path, &settings); err != nil {
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			return nil, err
		}
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	return settings, nil
}

func readJSONIfThere(path string, into any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, into)
}

// writeFileAtomic replaces path in one step, so Claude never reads half a file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".deepcove-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("sharedlogin: write %s: %w", path, err)
	}
	return nil
}
