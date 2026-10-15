package sharedlogin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signedToken makes a JWT the way OpenAI signs its tokens.
func signedToken(t *testing.T, key *rsa.PrivateKey, keyID, algorithm string, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	unsigned := encode(map[string]string{"alg": algorithm, "kid": keyID}) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// codexTokenClaims are an OpenAI access token's claims for account, valid for lifetime from now.
func codexTokenClaims(account string, lifetime time.Duration) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                            codexIssuer,
		"iat":                            now.Unix(),
		"exp":                            now.Add(lifetime).Unix(),
		"https://api.openai.com/auth":    map[string]string{"chatgpt_account_id": account},
		"https://api.openai.com/profile": map[string]string{"email": account + "@example.com"},
	}
}

// keysServer publishes key under keyID, the way OpenAI publishes its signing keys.
func keysServer(t *testing.T, key *rsa.PublicKey, keyID string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": keyID,
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCodexProvider_Account(t *testing.T) {
	openAI, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := keysServer(t, &openAI.PublicKey, "key-1")
	wrongIssuer := codexTokenClaims("account-1", time.Hour)
	wrongIssuer["iss"] = "https://evil.example"

	cases := []struct {
		name    string
		token   string
		wantID  string
		wantErr string
	}{
		{name: "a token OpenAI signed", token: signedToken(t, openAI, "key-1", "RS256", codexTokenClaims("account-1", time.Hour)), wantID: "account-1"},
		{name: "a token someone else signed", token: signedToken(t, stranger, "key-1", "RS256", codexTokenClaims("account-1", time.Hour)), wantErr: "did not sign"},
		{name: "a key OpenAI does not publish", token: signedToken(t, openAI, "key-2", "RS256", codexTokenClaims("account-1", time.Hour)), wantErr: "no signing key"},
		{name: "another algorithm", token: signedToken(t, openAI, "key-1", "none", codexTokenClaims("account-1", time.Hour)), wantErr: "not RS256"},
		{name: "an expired token", token: signedToken(t, openAI, "key-1", "RS256", codexTokenClaims("account-1", -time.Minute)), wantErr: "expired"},
		{name: "another issuer", token: signedToken(t, openAI, "key-1", "RS256", wrongIssuer), wantErr: "comes from"},
		{name: "no account", token: signedToken(t, openAI, "key-1", "RS256", codexTokenClaims("", time.Hour)), wantErr: "no ChatGPT account"},
		{name: "not a JWT", token: "opaque", wantErr: "not a JWT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &CodexProvider{Client: http.DefaultClient, KeysURL: keys.URL}
			account, err := provider.Account(context.Background(), tc.token)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || account.ID != tc.wantID || account.Name != tc.wantID+"@example.com" {
				t.Errorf("Account = %+v, %v, want %s", account, err, tc.wantID)
			}
		})
	}
}

func TestCodexProvider_Refresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	newAccess := signedToken(t, key, "key-1", "RS256", codexTokenClaims("account-1", time.Hour))
	cases := []struct {
		name        string
		reply       map[string]string
		wantRefresh string
		wantID      string
	}{
		{name: "new tokens replace the old ones", reply: map[string]string{"access_token": newAccess, "refresh_token": "refresh-2", "id_token": "id-2"}, wantRefresh: "refresh-2", wantID: "id-2"},
		{name: "a reply without a refresh token keeps the old one", reply: map[string]string{"access_token": newAccess}, wantRefresh: "refresh-1", wantID: "id-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asked map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&asked)
				_ = json.NewEncoder(w).Encode(tc.reply)
			}))
			defer server.Close()
			old, err := codexLogin(signedToken(t, key, "key-1", "RS256", codexTokenClaims("account-1", time.Minute)), "refresh-1", "id-1")
			if err != nil {
				t.Fatal(err)
			}

			provider := &CodexProvider{Client: server.Client(), TokenURL: server.URL}
			refreshed, err := provider.Refresh(context.Background(), old)
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			if asked["grant_type"] != "refresh_token" || asked["client_id"] != codexClientID || asked["refresh_token"] != "refresh-1" {
				t.Errorf("asked %v, want Codex's own refresh request", asked)
			}
			fields, _ := codexFieldsOf(refreshed)
			if refreshed.AccessToken != newAccess || refreshed.RefreshToken != tc.wantRefresh || fields.IDToken != tc.wantID {
				t.Errorf("refreshed to %q, %q, id %q; want the new access token, %q, %q",
					refreshed.AccessToken, refreshed.RefreshToken, fields.IDToken, tc.wantRefresh, tc.wantID)
			}
		})
	}
}

func TestCodexLogin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		lifetime time.Duration
		want     time.Duration
	}{
		{name: "a token that expires within a week is refreshed before it expires", lifetime: time.Hour, want: time.Hour},
		{name: "a longer token is refreshed within a week, before Codex would do it itself", lifetime: 10 * 24 * time.Hour, want: codexRefreshEvery},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			login, err := codexLogin(signedToken(t, key, "key-1", "RS256", codexTokenClaims("account-1", tc.lifetime)), "", "id-1")
			if err != nil {
				t.Fatalf("codexLogin: %v", err)
			}
			if got := time.Until(login.ExpiresAt).Round(time.Minute); got != tc.want {
				t.Errorf("refresh due in %v, want %v", got, tc.want)
			}
			if login.AccountID != "account-1" || login.AccountName != "account-1@example.com" {
				t.Errorf("account %q %q, want account-1", login.AccountID, login.AccountName)
			}
		})
	}
}
