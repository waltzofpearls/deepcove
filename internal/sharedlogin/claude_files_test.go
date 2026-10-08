package sharedlogin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeLoginFiles_WriteThenRead(t *testing.T) {
	home := t.TempDir()
	files := ClaudeLoginFiles{Home: home}
	// The session's own settings, which must survive.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"mine":{}},"numStartups":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	login, err := ClaudeLogin(ClaudeOAuth{AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Scopes: []string{"user:inference"}},
		json.RawMessage(`{"accountUuid":"account-1"}`))
	if err != nil {
		t.Fatal(err)
	}

	if err := files.WriteTokens(login); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	if err := files.WriteAccount(login); err != nil {
		t.Fatalf("WriteAccount: %v", err)
	}
	got, onboarded, err := files.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.AccessToken != "a" || got.AccountID != "account-1" || !onboarded {
		t.Errorf("read %+v onboarded %v, want the login back and setup done", got, onboarded)
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var kept map[string]json.RawMessage
	_ = json.Unmarshal(settings, &kept)
	if _, ok := kept["mcpServers"]; !ok || string(kept["numStartups"]) != "3" {
		t.Errorf("settings = %s, want the session's own keys kept", settings)
	}
	if info, _ := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestClaudeLoginFiles_ReadWithNothingThere(t *testing.T) {
	got, onboarded, err := ClaudeLoginFiles{Home: t.TempDir()}.Read()
	if err != nil || got.AccessToken != "" || onboarded {
		t.Errorf("Read = %+v, %v, %v; want an empty login and no error", got, onboarded, err)
	}
}

func TestClaudeLoginFiles_RemoveTokens(t *testing.T) {
	files := ClaudeLoginFiles{Home: t.TempDir()}
	login, _ := ClaudeLogin(ClaudeOAuth{AccessToken: "a"}, json.RawMessage(`{"accountUuid":"account-1"}`))
	if err := files.WriteTokens(login); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := files.RemoveTokens(); err != nil {
			t.Fatalf("RemoveTokens: %v", err)
		}
	}
	if got, _, _ := files.Read(); got.AccessToken != "" {
		t.Errorf("access token = %q after removing, want none", got.AccessToken)
	}
}
