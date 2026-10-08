package sharedlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Claude Code keeps its tokens in ~/.claude/.credentials.json under "claudeAiOauth", and its account in
// ~/.claude.json under "oauthAccount".

const (
	// ClaudeTokenURL is where Claude Code itself refreshes its login.
	ClaudeTokenURL = "https://platform.claude.com/v1/oauth/token"
	// ClaudeProfileURL is where Claude Code itself asks which account a token belongs to.
	ClaudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	// claudeClientID is Claude Code's public OAuth client, the one its own /login uses.
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

// ClaudeOAuth is the "claudeAiOauth" object in Claude Code's .credentials.json.
type ClaudeOAuth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"` // milliseconds since 1970, as Claude writes it
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`
}

type claudeAgentFields struct {
	Scopes           []string        `json:"scopes"`
	SubscriptionType string          `json:"subscription_type,omitempty"`
	RateLimitTier    string          `json:"rate_limit_tier,omitempty"`
	Account          json.RawMessage `json:"account,omitempty"`
}

// ClaudeLogin turns Claude's login into a Login. account is the "oauthAccount" object, or nil.
func ClaudeLogin(oauth ClaudeOAuth, account json.RawMessage) (Login, error) {
	fields, err := json.Marshal(claudeAgentFields{
		Scopes:           oauth.Scopes,
		SubscriptionType: oauth.SubscriptionType,
		RateLimitTier:    oauth.RateLimitTier,
		Account:          account,
	})
	if err != nil {
		return Login{}, err
	}
	return Login{
		AccessToken:  oauth.AccessToken,
		RefreshToken: oauth.RefreshToken,
		ExpiresAt:    time.UnixMilli(oauth.ExpiresAt),
		AccountID:    claudeAccountID(account),
		AccountName:  claudeAccountEmail(account),
		AgentFields:  fields,
	}, nil
}

// ClaudeFiles turns a Login back into what Claude reads: the "claudeAiOauth" object, and the
// "oauthAccount" object for ~/.claude.json.
func ClaudeFiles(login Login) (ClaudeOAuth, json.RawMessage, error) {
	fields, err := claudeFieldsOf(login)
	if err != nil {
		return ClaudeOAuth{}, nil, err
	}
	return ClaudeOAuth{
		AccessToken:      login.AccessToken,
		RefreshToken:     login.RefreshToken,
		ExpiresAt:        login.ExpiresAt.UnixMilli(),
		Scopes:           fields.Scopes,
		SubscriptionType: fields.SubscriptionType,
		RateLimitTier:    fields.RateLimitTier,
	}, fields.Account, nil
}

func claudeFieldsOf(login Login) (claudeAgentFields, error) {
	var fields claudeAgentFields
	if len(login.AgentFields) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(login.AgentFields, &fields); err != nil {
		return claudeAgentFields{}, fmt.Errorf("sharedlogin: read the Claude login: %w", err)
	}
	return fields, nil
}

func claudeAccountID(account json.RawMessage) string {
	return parseClaudeAccount(account).AccountUUID
}

func claudeAccountEmail(account json.RawMessage) string {
	return parseClaudeAccount(account).EmailAddress
}

type claudeAccount struct {
	AccountUUID  string `json:"accountUuid"`
	EmailAddress string `json:"emailAddress"`
}

func parseClaudeAccount(account json.RawMessage) claudeAccount {
	var parsed claudeAccount
	if len(account) == 0 || json.Unmarshal(account, &parsed) != nil {
		return claudeAccount{}
	}
	return parsed
}

// ClaudeProvider refreshes and checks a Claude login the way Claude Code does.
type ClaudeProvider struct {
	Client     *http.Client
	TokenURL   string
	ProfileURL string
}

// Refresh trades the refresh token for a new login. A reply without a refresh token keeps the old one,
// as Claude Code does.
func (r ClaudeProvider) Refresh(ctx context.Context, login Login) (Login, error) {
	fields, err := claudeFieldsOf(login)
	if err != nil {
		return Login{}, err
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": login.RefreshToken,
		"client_id":     claudeClientID,
		"scope":         strings.Join(fields.Scopes, " "),
	})
	if err != nil {
		return Login{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.TokenURL, bytes.NewReader(body))
	if err != nil {
		return Login{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.Client.Do(request)
	if err != nil {
		return Login{}, fmt.Errorf("sharedlogin: refresh the Claude login: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Login{}, fmt.Errorf("sharedlogin: refresh the Claude login: %s", response.Status)
	}
	var reply struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return Login{}, fmt.Errorf("sharedlogin: read the refreshed Claude login: %w", err)
	}
	if reply.AccessToken == "" {
		return Login{}, fmt.Errorf("sharedlogin: the refreshed Claude login has no access token")
	}
	refreshed := login
	refreshed.AccessToken = reply.AccessToken
	if reply.RefreshToken != "" {
		refreshed.RefreshToken = reply.RefreshToken
	}
	refreshed.ExpiresAt = time.Now().Add(time.Duration(reply.ExpiresIn) * time.Second)
	return refreshed, nil
}

// Account asks Anthropic which account the access token belongs to.
func (r ClaudeProvider) Account(ctx context.Context, accessToken string) (Account, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.ProfileURL, nil)
	if err != nil {
		return Account{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.Client.Do(request)
	if err != nil {
		return Account{}, fmt.Errorf("sharedlogin: check the Claude account: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Account{}, fmt.Errorf("sharedlogin: check the Claude account: %s", response.Status)
	}
	var profile struct {
		Account struct {
			UUID  string `json:"uuid"`
			Email string `json:"email"`
		} `json:"account"`
	}
	if err := json.NewDecoder(response.Body).Decode(&profile); err != nil {
		return Account{}, fmt.Errorf("sharedlogin: read the Claude account: %w", err)
	}
	if profile.Account.UUID == "" {
		return Account{}, fmt.Errorf("sharedlogin: Anthropic named no Claude account for that token")
	}
	return Account{ID: profile.Account.UUID, Name: profile.Account.Email}, nil
}
