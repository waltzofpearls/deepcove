package sharedlogin

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexLoginFiles(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	access := signedToken(t, key, "key-1", "RS256", codexTokenClaims("account-1", time.Hour))
	cases := []struct {
		name         string
		refreshToken string
	}{
		{name: "the shared copy, which has no refresh token", refreshToken: ""},
		{name: "a login the session made itself", refreshToken: "refresh-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			files := CodexLoginFiles{Home: home}
			// Something else Codex keeps in the same file has to survive.
			if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(files.TokenPath(), []byte(`{"personal_access_token":"kept"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			login, err := codexLogin(access, tc.refreshToken, "id-1")
			if err != nil {
				t.Fatal(err)
			}

			if err := files.WriteTokens(login); err != nil {
				t.Fatalf("WriteTokens: %v", err)
			}
			var written struct {
				AuthMode    string      `json:"auth_mode"`
				Tokens      codexTokens `json:"tokens"`
				LastRefresh time.Time   `json:"last_refresh"`
				Kept        string      `json:"personal_access_token"`
			}
			data, _ := os.ReadFile(files.TokenPath())
			if err := json.Unmarshal(data, &written); err != nil {
				t.Fatalf("auth.json: %v", err)
			}
			if written.AuthMode != "chatgpt" || written.Tokens.IDToken != "id-1" || written.Tokens.AccountID != "account-1" || written.Kept != "kept" {
				t.Errorf("auth.json = %s", data)
			}
			if written.Tokens.RefreshToken == "" {
				t.Error("no refresh token at all, and Codex will not load a login without one")
			}
			if time.Since(written.LastRefresh) > time.Minute {
				t.Errorf("last_refresh %v, want now so Codex does not refresh it itself", written.LastRefresh)
			}

			read, onboarded, err := files.Read()
			if err != nil || !onboarded {
				t.Fatalf("Read = %v, %v", onboarded, err)
			}
			if read.AccessToken != access || read.RefreshToken != tc.refreshToken || read.AccountID != "account-1" {
				t.Errorf("read back %+v, want the same login with refresh token %q", read, tc.refreshToken)
			}

			if err := files.RemoveTokens(); err != nil {
				t.Fatal(err)
			}
			if gone, _, _ := files.Read(); gone.AccessToken != "" {
				t.Error("still logged in after RemoveTokens")
			}
		})
	}
}
