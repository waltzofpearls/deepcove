package sharedlogin

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Codex keeps its whole login in ~/.codex/auth.json: an access token and an id token, both JWTs signed by
// OpenAI, and a refresh token.

const (
	// CodexTokenURL is where Codex itself refreshes its login.
	CodexTokenURL = "https://auth.openai.com/oauth/token"
	// CodexKeysURL is where OpenAI publishes the keys its tokens are signed with.
	CodexKeysURL = "https://auth.openai.com/.well-known/jwks.json"
	// codexClientID is Codex's public OAuth client, the one its own login uses.
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexIssuer   = "https://auth.openai.com"
	// codexRefreshEvery is how old a login may get before the host refreshes it. Codex refreshes on its
	// own a login older than 8 days, and in a session that would spend a refresh token it does not have.
	codexRefreshEvery = 7 * 24 * time.Hour
	// codexKeysCheckEvery limits how often an unknown signing key sends the host back to OpenAI.
	codexKeysCheckEvery = time.Minute
)

type codexAgentFields struct {
	IDToken string `json:"id_token"`
}

// codexClaims are the parts of an OpenAI access token deepcove reads.
type codexClaims struct {
	Issuer    string `json:"iss"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	Auth      struct {
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
}

// codexLogin turns Codex's tokens into a Login. It reads the access token without checking its signature,
// which is fine for the session's own copy: the host checks the account itself, with Account.
func codexLogin(accessToken, refreshToken, idToken string) (Login, error) {
	var claims codexClaims
	if err := decodeJWTPart(accessToken, 1, &claims); err != nil {
		return Login{}, fmt.Errorf("sharedlogin: read the Codex login: %w", err)
	}
	fields, err := json.Marshal(codexAgentFields{IDToken: idToken})
	if err != nil {
		return Login{}, err
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	if refreshBy := time.Unix(claims.IssuedAt, 0).Add(codexRefreshEvery); claims.IssuedAt != 0 && refreshBy.Before(expiresAt) {
		expiresAt = refreshBy
	}
	return Login{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    claims.Auth.AccountID,
		AccountName:  claims.Profile.Email,
		AgentFields:  fields,
	}, nil
}

func codexFieldsOf(login Login) (codexAgentFields, error) {
	var fields codexAgentFields
	if len(login.AgentFields) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(login.AgentFields, &fields); err != nil {
		return codexAgentFields{}, fmt.Errorf("sharedlogin: read the Codex login: %w", err)
	}
	return fields, nil
}

// CodexProvider refreshes a Codex login the way Codex does, and checks its account against OpenAI's
// signature on the token.
type CodexProvider struct {
	Client   *http.Client
	TokenURL string
	KeysURL  string

	mu            sync.Mutex
	keys          map[string]*rsa.PublicKey
	keysCheckedAt time.Time
}

// Refresh trades the refresh token for a new login. A reply missing a token keeps the old one.
func (p *CodexProvider) Refresh(ctx context.Context, login Login) (Login, error) {
	fields, err := codexFieldsOf(login)
	if err != nil {
		return Login{}, err
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     codexClientID,
		"refresh_token": login.RefreshToken,
	})
	if err != nil {
		return Login{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, bytes.NewReader(body))
	if err != nil {
		return Login{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(request)
	if err != nil {
		return Login{}, fmt.Errorf("sharedlogin: refresh the Codex login: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Login{}, fmt.Errorf("sharedlogin: refresh the Codex login: %s", response.Status)
	}
	var reply struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return Login{}, fmt.Errorf("sharedlogin: read the refreshed Codex login: %w", err)
	}
	if reply.AccessToken == "" {
		return Login{}, errors.New("sharedlogin: the refreshed Codex login has no access token")
	}
	return codexLogin(reply.AccessToken, cmp.Or(reply.RefreshToken, login.RefreshToken), cmp.Or(reply.IDToken, fields.IDToken))
}

// Account reads the account from the access token once OpenAI's signature on it checks out, so a session
// cannot claim an account it does not have.
func (p *CodexProvider) Account(ctx context.Context, accessToken string) (Account, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return Account{}, errors.New("sharedlogin: the Codex access token is not a JWT")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := decodeJWTPart(accessToken, 0, &header); err != nil {
		return Account{}, fmt.Errorf("sharedlogin: read the Codex access token: %w", err)
	}
	if header.Algorithm != "RS256" {
		return Account{}, fmt.Errorf("sharedlogin: the Codex access token is signed with %q, not RS256", header.Algorithm)
	}
	key, err := p.signingKey(ctx, header.KeyID)
	if err != nil {
		return Account{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Account{}, fmt.Errorf("sharedlogin: read the Codex token's signature: %w", err)
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, signed[:], signature); err != nil {
		return Account{}, errors.New("sharedlogin: OpenAI did not sign this Codex access token")
	}
	var claims codexClaims
	if err := decodeJWTPart(accessToken, 1, &claims); err != nil {
		return Account{}, fmt.Errorf("sharedlogin: read the Codex access token: %w", err)
	}
	switch {
	case claims.Issuer != codexIssuer:
		return Account{}, fmt.Errorf("sharedlogin: the Codex access token comes from %q", claims.Issuer)
	case time.Now().After(time.Unix(claims.ExpiresAt, 0)):
		return Account{}, errors.New("sharedlogin: the Codex access token has expired")
	case claims.Auth.AccountID == "":
		return Account{}, errors.New("sharedlogin: the Codex access token names no ChatGPT account")
	}
	return Account{ID: claims.Auth.AccountID, Name: claims.Profile.Email}, nil
}

// signingKey is OpenAI's key keyID. The keys are fetched again for a key not seen yet, since OpenAI
// rotates them, but at most once a minute.
func (p *CodexProvider) signingKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if key, ok := p.keys[keyID]; ok {
		return key, nil
	}
	if time.Since(p.keysCheckedAt) >= codexKeysCheckEvery {
		keys, err := p.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		p.keys, p.keysCheckedAt = keys, time.Now()
	}
	if key, ok := p.keys[keyID]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("sharedlogin: OpenAI publishes no signing key %q", keyID)
}

func (p *CodexProvider) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.KeysURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("sharedlogin: fetch OpenAI's signing keys: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sharedlogin: fetch OpenAI's signing keys: %s", response.Status)
	}
	var set struct {
		Keys []struct {
			KeyType string `json:"kty"`
			KeyID   string `json:"kid"`
			N       string `json:"n"`
			E       string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("sharedlogin: read OpenAI's signing keys: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, key := range set.Keys {
		if key.KeyType != "RSA" {
			continue
		}
		modulus, nErr := base64.RawURLEncoding.DecodeString(key.N)
		exponent, eErr := base64.RawURLEncoding.DecodeString(key.E)
		if nErr != nil || eErr != nil {
			continue
		}
		keys[key.KeyID] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(modulus),
			E: int(new(big.Int).SetBytes(exponent).Int64()),
		}
	}
	return keys, nil
}

// decodeJWTPart reads part index of a JWT, 0 the header and 1 the claims, into into.
func decodeJWTPart(token string, index int, into any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[index])
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}
