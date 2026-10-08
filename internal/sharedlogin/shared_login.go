// Package sharedlogin keeps the user's own agent login on the host and hands each session a copy
// without the refresh token. Only the host refreshes, so sessions cannot log each other out.
package sharedlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Login is one agent's login, in the fields every agent has.
type Login struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccountID    string    `json:"account_id"`
	// AccountName is what the user knows the account by, like its email.
	AccountName string `json:"account_name,omitempty"`
	// AgentFields is what only this agent needs back, like Claude's scopes. The keeper never reads it.
	AgentFields json.RawMessage `json:"agent_fields,omitempty"`
}

// OtherAccounts is what the host knows of sessions logged in to another account than the shared one.
type OtherAccounts struct {
	// Waiting is the one login waiting for the user to choose. Any other account waits its turn.
	Waiting *WaitingLogin `json:"waiting,omitempty"`
	// Kept lists the accounts the user chose not to share, so they are not asked about again.
	Kept []string `json:"kept,omitempty"`
}

// WaitingLogin is another account's login, waiting for the user to choose.
type WaitingLogin struct {
	Login Login `json:"login"`
	// SandboxID is the machine whose session made it. That session shows the prompt.
	SandboxID string `json:"sandbox_id"`
}

// Store keeps one login per agent, and what it knows of other accounts.
type Store interface {
	Load(agent string) (Login, bool, error)
	Save(agent string, login Login) error
	LoadOtherAccounts(agent string) (OtherAccounts, error)
	SaveOtherAccounts(agent string, others OtherAccounts) error
}

// Provider is what each agent supplies: how to trade its refresh token for a new login, and how to
// ask its vendor which account an access token belongs to.
type Provider interface {
	Refresh(ctx context.Context, login Login) (Login, error)
	// Account asks the vendor rather than the session, since a session can claim any account it likes.
	Account(ctx context.Context, accessToken string) (Account, error)
}

// Account is who a login belongs to, as the vendor says.
type Account struct {
	ID   string
	Name string
}

var (
	// ErrNoLogin means nobody has logged in yet. The session logs in on its own and offers the login.
	ErrNoLogin = errors.New("sharedlogin: no shared login yet")
	// ErrDifferentAccount means a session logged in to another account than the shared one.
	ErrDifferentAccount = errors.New("sharedlogin: that login is for a different account")
	// ErrAccountMismatch means a session offered a login for another account than it said.
	ErrAccountMismatch = errors.New("sharedlogin: that login belongs to another account than it says")
	// ErrNoWaitingLogin means there is no other account waiting for the user to choose.
	ErrNoWaitingLogin = errors.New("sharedlogin: no other account is waiting")
)

// refreshWindow is how long before expiry the host refreshes, so a session never holds an expired token.
const refreshWindow = 30 * time.Minute

// Keeper is the one place a shared login is read, refreshed and replaced.
type Keeper struct {
	store     Store
	providers map[string]Provider
	now       func() time.Time
	// isSharing says whether a session shares its login right now. Nil means every session does.
	isSharing func(agent, sessionID string) (bool, error)
	// mu makes refreshes one at a time. A second one would spend the token the first just replaced.
	mu sync.Mutex
	// accountsMu guards verifiedAccounts, the vendor's answer for each access token.
	accountsMu       sync.Mutex
	verifiedAccounts map[string]Account
	// failedOffers is when each failed offer may be tried again, by access token. Guarded by mu.
	failedOffers map[string]time.Time
}

// NewKeeper keeps logins in store, refreshed and checked by each agent's provider.
func NewKeeper(store Store, providers map[string]Provider) *Keeper {
	return &Keeper{
		store: store, providers: providers, now: time.Now,
		verifiedAccounts: map[string]Account{}, failedOffers: map[string]time.Time{},
	}
}

// SetSharingCheck has every request ask isSharing, so a sharing switch reaches a running session.
func (k *Keeper) SetSharingCheck(isSharing func(agent, sessionID string) (bool, error)) {
	k.isSharing = isSharing
}

// CanShare reports whether agent has a shared login at all.
func (k *Keeper) CanShare(agent string) bool {
	_, ok := k.providers[agent]
	return ok
}

// ForSession returns the login a session gets: fresh, and without a refresh token.
func (k *Keeper) ForSession(ctx context.Context, agent string) (Login, error) {
	provider, ok := k.providers[agent]
	if !ok {
		return Login{}, fmt.Errorf("sharedlogin: %s has no shared login", agent)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	login, ok, err := k.store.Load(agent)
	if err != nil {
		return Login{}, err
	}
	if !ok {
		return Login{}, ErrNoLogin
	}
	if k.now().Add(refreshWindow).After(login.ExpiresAt) {
		refreshed, err := provider.Refresh(ctx, login)
		if err != nil {
			return Login{}, err
		}
		if err := k.store.Save(agent, refreshed); err != nil {
			return Login{}, err
		}
		login = refreshed
	}
	login.RefreshToken = ""
	return login, nil
}

// Offer takes a login a session made itself. The first login, or a newer one for the same account,
// becomes the shared one. Another account waits for the user to choose.
func (k *Keeper) Offer(ctx context.Context, agent, sandboxID string, offered Login) error {
	if offered.RefreshToken == "" {
		// A copy the host handed out, or no login at all.
		return nil
	}
	provider, ok := k.providers[agent]
	if !ok {
		return fmt.Errorf("sharedlogin: %s has no shared login", agent)
	}
	account, err := k.accountOf(ctx, provider, offered.AccessToken)
	if err != nil {
		return err
	}
	if account.ID != offered.AccountID {
		return ErrAccountMismatch
	}
	offered.AccountName = account.Name
	k.mu.Lock()
	defer k.mu.Unlock()
	stored, ok, err := k.store.Load(agent)
	if err != nil {
		return err
	}
	if ok && stored.AccountID != offered.AccountID {
		if err := k.holdForUser(agent, sandboxID, offered); err != nil {
			return err
		}
		return ErrDifferentAccount
	}
	// An older login would bring back a refresh token already spent.
	if ok && !offered.ExpiresAt.After(stored.ExpiresAt) {
		return nil
	}
	refreshed, err := k.refreshOffered(ctx, provider, offered)
	if err != nil {
		return err
	}
	return k.store.Save(agent, refreshed)
}

const offerRetryAfter = 5 * time.Minute

// refreshOffered keeps only what the vendor returns, and only for the account the session claimed. A
// session controls every field it offers. Called with mu held.
func (k *Keeper) refreshOffered(ctx context.Context, provider Provider, offered Login) (Login, error) {
	if retryAt, failed := k.failedOffers[offered.AccessToken]; failed && k.now().Before(retryAt) {
		return Login{}, fmt.Errorf("sharedlogin: that login did not refresh, trying again after %s", retryAt.Format(time.Kitchen))
	}
	refreshed, err := provider.Refresh(ctx, offered)
	if err != nil {
		k.rememberFailedOffer(offered)
		return Login{}, fmt.Errorf("sharedlogin: the offered login does not refresh: %w", err)
	}
	account, err := k.accountOf(ctx, provider, refreshed.AccessToken)
	if err != nil {
		k.rememberFailedOffer(offered)
		return Login{}, err
	}
	if account.ID != offered.AccountID {
		k.rememberFailedOffer(offered)
		return Login{}, ErrAccountMismatch
	}
	delete(k.failedOffers, offered.AccessToken)
	refreshed.AccountName = account.Name
	return refreshed, nil
}

func (k *Keeper) rememberFailedOffer(offered Login) {
	if len(k.failedOffers) >= maxVerifiedAccounts {
		clear(k.failedOffers)
	}
	k.failedOffers[offered.AccessToken] = k.now().Add(offerRetryAfter)
}

// holdForUser keeps another account's login until the user chooses. Each new offer keeps it fresh.
func (k *Keeper) holdForUser(agent, sandboxID string, offered Login) error {
	others, err := k.store.LoadOtherAccounts(agent)
	if err != nil {
		return err
	}
	if slices.Contains(others.Kept, offered.AccountID) {
		return nil
	}
	if waiting := others.Waiting; waiting != nil {
		if waiting.Login.AccountID != offered.AccountID {
			return nil
		}
		// The same login from a recreated machine only moves the prompt. Another login replaces it only
		// when newer, or two sessions would flip it every round.
		if waiting.Login.AccessToken == offered.AccessToken {
			if waiting.SandboxID == sandboxID {
				return nil
			}
			others.Waiting = &WaitingLogin{Login: waiting.Login, SandboxID: sandboxID}
			return k.store.SaveOtherAccounts(agent, others)
		}
		if !offered.ExpiresAt.After(waiting.Login.ExpiresAt) {
			return nil
		}
	}
	others.Waiting = &WaitingLogin{Login: offered, SandboxID: sandboxID}
	return k.store.SaveOtherAccounts(agent, others)
}

// Status is what the user sees of one agent's shared login.
type Status struct {
	LoggedIn bool `json:"logged_in"`
	// Account is the shared account's name. It can be empty even when logged in.
	Account string         `json:"account"`
	Waiting *WaitingStatus `json:"waiting"`
}

// WaitingStatus is the part of a waiting login the user sees.
type WaitingStatus struct {
	Account   string `json:"account"`
	SandboxID string `json:"sandbox_id"`
}

// Status returns the shared account, and another account waiting for the user if there is one.
func (k *Keeper) Status(agent string) (Status, error) {
	if _, ok := k.providers[agent]; !ok {
		return Status{}, fmt.Errorf("sharedlogin: %s has no shared login", agent)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var status Status
	login, ok, err := k.store.Load(agent)
	if err != nil {
		return Status{}, err
	}
	if ok {
		status.LoggedIn, status.Account = true, login.AccountName
	}
	others, err := k.store.LoadOtherAccounts(agent)
	if err != nil {
		return Status{}, err
	}
	if waiting := others.Waiting; waiting != nil {
		status.Waiting = &WaitingStatus{Account: waiting.Login.AccountName, SandboxID: waiting.SandboxID}
	}
	return status, nil
}

// SwitchToWaitingLogin makes the waiting account the shared one. Every sharing session picks it up on
// its next round.
func (k *Keeper) SwitchToWaitingLogin(ctx context.Context, agent string) error {
	provider, ok := k.providers[agent]
	if !ok {
		return fmt.Errorf("sharedlogin: %s has no shared login", agent)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	others, err := k.store.LoadOtherAccounts(agent)
	if err != nil {
		return err
	}
	if others.Waiting == nil {
		return ErrNoWaitingLogin
	}
	refreshed, err := k.refreshOffered(ctx, provider, others.Waiting.Login)
	if err != nil {
		return err
	}
	if err := k.store.Save(agent, refreshed); err != nil {
		return err
	}
	others.Waiting = nil
	return k.store.SaveOtherAccounts(agent, others)
}

// KeepSharedLogin keeps the shared account. The session that logged in to the other one keeps its own
// login, and the host stops asking about that account.
func (k *Keeper) KeepSharedLogin(agent string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	others, err := k.store.LoadOtherAccounts(agent)
	if err != nil {
		return err
	}
	if others.Waiting == nil {
		return ErrNoWaitingLogin
	}
	others.Kept = append(others.Kept, others.Waiting.Login.AccountID)
	others.Waiting = nil
	return k.store.SaveOtherAccounts(agent, others)
}

// maxVerifiedAccounts bounds both caches. Only a handful of tokens are live at once.
const maxVerifiedAccounts = 64

func (k *Keeper) accountOf(ctx context.Context, provider Provider, accessToken string) (Account, error) {
	k.accountsMu.Lock()
	account, ok := k.verifiedAccounts[accessToken]
	k.accountsMu.Unlock()
	if ok {
		return account, nil
	}
	account, err := provider.Account(ctx, accessToken)
	if err != nil {
		return Account{}, err
	}
	k.accountsMu.Lock()
	defer k.accountsMu.Unlock()
	if len(k.verifiedAccounts) >= maxVerifiedAccounts {
		clear(k.verifiedAccounts)
	}
	k.verifiedAccounts[accessToken] = account
	return account, nil
}
