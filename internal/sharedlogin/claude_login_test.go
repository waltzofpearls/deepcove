package sharedlogin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClaudeLogin_RoundTrip(t *testing.T) {
	oauth := ClaudeOAuth{
		AccessToken: "a", RefreshToken: "r", ExpiresAt: 1791000000000,
		Scopes: []string{"user:inference", "user:profile"}, SubscriptionType: "max",
	}
	account := json.RawMessage(`{"accountUuid":"account-1","emailAddress":"me@example.com"}`)

	login, err := ClaudeLogin(oauth, account)
	if err != nil {
		t.Fatalf("ClaudeLogin: %v", err)
	}
	if login.AccountID != "account-1" || login.AccountName != "me@example.com" {
		t.Errorf("account = %q %q, want account-1 me@example.com", login.AccountID, login.AccountName)
	}
	gotOAuth, gotAccount, err := ClaudeFiles(login)
	if err != nil {
		t.Fatalf("ClaudeFiles: %v", err)
	}
	if gotOAuth.AccessToken != "a" || gotOAuth.ExpiresAt != oauth.ExpiresAt || len(gotOAuth.Scopes) != 2 || gotOAuth.SubscriptionType != "max" {
		t.Errorf("oauth = %+v, want it back as it went in", gotOAuth)
	}
	if string(gotAccount) != string(account) {
		t.Errorf("account = %s, want %s", gotAccount, account)
	}
}

func TestClaudeProvider_Refresh(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		reply       string
		wantErr     bool
		wantAccess  string
		wantRefresh string
	}{
		{name: "new tokens", status: 200, reply: `{"access_token":"new-a","refresh_token":"new-r","expires_in":28800}`, wantAccess: "new-a", wantRefresh: "new-r"},
		{name: "no refresh token in reply", status: 200, reply: `{"access_token":"new-a","expires_in":28800}`, wantAccess: "new-a", wantRefresh: "old-r"},
		{name: "server says 401", status: 401, reply: `{}`, wantErr: true},
		{name: "no access token in reply", status: 200, reply: `{"expires_in":28800}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("content type = %q, want application/json", r.Header.Get("Content-Type"))
				}
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &sent)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()
			login, _ := ClaudeLogin(ClaudeOAuth{AccessToken: "old-a", RefreshToken: "old-r", Scopes: []string{"user:inference"}}, nil)

			refreshed, err := ClaudeProvider{Client: server.Client(), TokenURL: server.URL}.Refresh(context.Background(), login)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if sent["grant_type"] != "refresh_token" || sent["refresh_token"] != "old-r" || sent["client_id"] != claudeClientID || sent["scope"] != "user:inference" {
				t.Errorf("sent %v, want Claude Code's refresh request", sent)
			}
			if tc.wantErr {
				return
			}
			if refreshed.AccessToken != tc.wantAccess || refreshed.RefreshToken != tc.wantRefresh {
				t.Errorf("got access %q refresh %q, want %q and %q", refreshed.AccessToken, refreshed.RefreshToken, tc.wantAccess, tc.wantRefresh)
			}
			if until := time.Until(refreshed.ExpiresAt); until < 7*time.Hour || until > 9*time.Hour {
				t.Errorf("expires in %s, want about 8h from expires_in", until)
			}
		})
	}
}

func TestClaudeProvider_Account(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		reply   string
		want    Account
		wantErr bool
	}{
		{name: "an account", status: 200, reply: `{"account":{"uuid":"account-1","email":"me@example.com"},"organization":{"uuid":"org-1"}}`, want: Account{ID: "account-1", Name: "me@example.com"}},
		{name: "a token Anthropic does not accept", status: 401, reply: `{}`, wantErr: true},
		{name: "no account in the reply", status: 200, reply: `{"organization":{"uuid":"org-1"}}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()

			got, err := ClaudeProvider{Client: server.Client(), ProfileURL: server.URL}.Account(context.Background(), "token-1")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("Account = %+v, %v; want %+v, an error: %v", got, err, tc.want, tc.wantErr)
			}
			if gotAuth != "Bearer token-1" {
				t.Errorf("Authorization = %q, want the token as a bearer", gotAuth)
			}
		})
	}
}
