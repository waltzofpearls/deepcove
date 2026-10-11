package sharedlogin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	mu     sync.Mutex
	logins map[string]Login
	others map[string]OtherAccounts
}

func newMemoryStore() *memoryStore {
	return &memoryStore{logins: map[string]Login{}, others: map[string]OtherAccounts{}}
}

func (s *memoryStore) Load(agent string) (Login, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	login, ok := s.logins[agent]
	return login, ok, nil
}

func (s *memoryStore) Save(agent string, login Login) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logins[agent] = login
	return nil
}

func (s *memoryStore) LoadOtherAccounts(agent string) (OtherAccounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.others[agent], nil
}

func (s *memoryStore) SaveOtherAccounts(agent string, others OtherAccounts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.others[agent] = others
	return nil
}

// countingProvider hands out "refreshed" tokens. owners is which account each token belongs to. A
// refreshed token keeps its old owner, unless a test sets one for "refreshed-access".
type countingProvider struct {
	refreshes     atomic.Int32
	accountChecks atomic.Int32
	err           error
	mu            sync.Mutex
	owners        map[string]string
}

func (r *countingProvider) Account(_ context.Context, accessToken string) (Account, error) {
	r.accountChecks.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	owner, ok := r.owners[accessToken]
	if !ok {
		return Account{}, errors.New("the vendor does not know that token")
	}
	return Account{ID: owner, Name: owner + "@example.com"}, nil
}

func (r *countingProvider) Refresh(_ context.Context, login Login) (Login, error) {
	r.refreshes.Add(1)
	if r.err != nil {
		return Login{}, r.err
	}
	// Slow enough that sessions asking at once overlap.
	time.Sleep(10 * time.Millisecond)
	r.mu.Lock()
	if _, named := r.owners["refreshed-access"]; !named && r.owners != nil {
		r.owners["refreshed-access"] = r.owners[login.AccessToken]
	}
	r.mu.Unlock()
	login.AccessToken, login.RefreshToken = "refreshed-access", "refreshed-refresh"
	login.ExpiresAt = time.Now().Add(8 * time.Hour)
	return login, nil
}

func TestKeeper_ForSession(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name           string
		stored         *Login
		refreshErr     error
		wantErr        error
		wantRefreshes  int32
		wantAccess     string
		wantStoredRefr string
	}{
		{name: "no login yet", wantErr: ErrNoLogin},
		{
			name:           "far from expiry",
			stored:         &Login{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(4 * time.Hour)},
			wantAccess:     "a",
			wantStoredRefr: "r",
		},
		{
			name:           "about to expire",
			stored:         &Login{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(10 * time.Minute)},
			wantRefreshes:  1,
			wantAccess:     "refreshed-access",
			wantStoredRefr: "refreshed-refresh",
		},
		{
			name:           "refresh fails",
			stored:         &Login{AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(10 * time.Minute)},
			refreshErr:     errors.New("anthropic is down"),
			wantErr:        errors.New("anthropic is down"),
			wantRefreshes:  1,
			wantStoredRefr: "r",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			if tc.stored != nil {
				_ = store.Save("claude", *tc.stored)
			}
			provider := &countingProvider{err: tc.refreshErr}
			keeper := NewKeeper(store, map[string]Provider{"claude": provider})

			login, err := keeper.ForSession(context.Background(), "claude")
			if fmt.Sprint(err) != fmt.Sprint(tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got := provider.refreshes.Load(); got != tc.wantRefreshes {
				t.Errorf("refreshes = %d, want %d", got, tc.wantRefreshes)
			}
			if err == nil {
				if login.AccessToken != tc.wantAccess || login.RefreshToken != "" {
					t.Errorf("session got access %q refresh %q, want %q and no refresh token", login.AccessToken, login.RefreshToken, tc.wantAccess)
				}
			}
			if stored, ok, _ := store.Load("claude"); ok && stored.RefreshToken != tc.wantStoredRefr {
				t.Errorf("stored refresh token = %q, want %q", stored.RefreshToken, tc.wantStoredRefr)
			}
		})
	}
}

func TestKeeper_ForSession_RefreshesOnceForManySessions(t *testing.T) {
	store := newMemoryStore()
	_ = store.Save("claude", Login{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Minute)})
	provider := &countingProvider{}
	keeper := NewKeeper(store, map[string]Provider{"claude": provider})

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = keeper.ForSession(context.Background(), "claude")
		}()
	}
	wg.Wait()
	if got := provider.refreshes.Load(); got != 1 {
		t.Errorf("refreshes = %d, want 1: a second refresh spends a token the first already replaced", got)
	}
}

func TestKeeper_ForSession_UnknownAgent(t *testing.T) {
	keeper := NewKeeper(newMemoryStore(), map[string]Provider{})
	if _, err := keeper.ForSession(context.Background(), "codex"); err == nil {
		t.Error("ForSession succeeded for an agent with no shared login")
	}
}

func TestKeeper_Offer(t *testing.T) {
	now := time.Now()
	stored := Login{AccessToken: "a", RefreshToken: "r", AccountID: "account-1", ExpiresAt: now.Add(2 * time.Hour)}
	cases := []struct {
		name             string
		stored           *Login
		offered          Login
		realAccount      string
		refreshedAccount string
		refreshErr       error
		wantErr          error
		wantStoredA      string
		wantWaitingA     string
	}{
		{name: "a session lying about its account", stored: &stored, offered: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-1", ExpiresAt: now.Add(8 * time.Hour)}, realAccount: "account-9", wantErr: ErrAccountMismatch, wantStoredA: "a"},
		{name: "first login, kept as the vendor refreshed it", offered: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-1", ExpiresAt: now.Add(8 * time.Hour)}, wantStoredA: "refreshed-access"},
		{name: "same account, newer", stored: &stored, offered: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-1", ExpiresAt: now.Add(8 * time.Hour)}, wantStoredA: "refreshed-access"},
		{name: "a refresh token from another account", stored: &stored, offered: Login{AccessToken: "b", RefreshToken: "theirs", AccountID: "account-1", ExpiresAt: now.Add(8 * time.Hour)}, refreshedAccount: "account-9", wantErr: errAnyError, wantStoredA: "a"},
		{name: "a login that does not refresh", stored: &stored, offered: Login{AccessToken: "b", RefreshToken: "made-up", AccountID: "account-1", ExpiresAt: now.Add(100 * 24 * time.Hour)}, refreshErr: errors.New("invalid_grant"), wantErr: errAnyError, wantStoredA: "a"},
		{name: "same account, older", stored: &stored, offered: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-1", ExpiresAt: now.Add(time.Hour)}, wantStoredA: "a"},
		{name: "another account", stored: &stored, offered: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2", ExpiresAt: now.Add(8 * time.Hour)}, wantErr: ErrDifferentAccount, wantStoredA: "a", wantWaitingA: "b"},
		{name: "a copy with no refresh token", stored: &stored, offered: Login{AccessToken: "b", AccountID: "account-2"}, wantStoredA: "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			if tc.stored != nil {
				_ = store.Save("claude", *tc.stored)
			}
			owner := tc.offered.AccountID
			if tc.realAccount != "" {
				owner = tc.realAccount
			}
			owners := map[string]string{tc.offered.AccessToken: owner}
			if tc.refreshedAccount != "" {
				owners["refreshed-access"] = tc.refreshedAccount
			}
			provider := &countingProvider{owners: owners, err: tc.refreshErr}
			keeper := NewKeeper(store, map[string]Provider{"claude": provider})

			err := keeper.Offer(context.Background(), "claude", "machine-1", tc.offered)
			if tc.wantErr == errAnyError {
				if err == nil {
					t.Fatal("Offer succeeded, want an error")
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Offer err = %v, want %v", err, tc.wantErr)
			}
			got, _, _ := store.Load("claude")
			if got.AccessToken != tc.wantStoredA {
				t.Errorf("stored access token = %q, want %q", got.AccessToken, tc.wantStoredA)
			}
			if got.AccessToken == "refreshed-access" && got.AccountName != owner+"@example.com" {
				t.Errorf("stored account name = %q, want the vendor's %q", got.AccountName, owner+"@example.com")
			}
			if got := waitingAccessToken(store); got != tc.wantWaitingA {
				t.Errorf("waiting access token = %q, want %q", got, tc.wantWaitingA)
			}
		})
	}
}

func waitingAccessToken(store *memoryStore) string {
	others, _ := store.LoadOtherAccounts("claude")
	if others.Waiting == nil {
		return ""
	}
	return others.Waiting.Login.AccessToken
}

func TestKeeper_Offer_AnotherAccount(t *testing.T) {
	now := time.Now()
	shared := Login{AccessToken: "a", RefreshToken: "r", AccountID: "account-1", ExpiresAt: now.Add(2 * time.Hour)}
	waiting := WaitingLogin{
		Login:     Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2", ExpiresAt: now.Add(4 * time.Hour)},
		SandboxID: "machine-1",
	}
	cases := []struct {
		name          string
		others        OtherAccounts
		sandboxID     string
		offered       Login
		wantWaitingA  string
		wantSandboxID string
	}{
		{
			name:          "nothing waiting",
			sandboxID:     "machine-1",
			offered:       waiting.Login,
			wantWaitingA:  "b",
			wantSandboxID: "machine-1",
		},
		{
			name:          "the session refreshed its own login",
			others:        OtherAccounts{Waiting: &waiting},
			sandboxID:     "machine-1",
			offered:       Login{AccessToken: "c", RefreshToken: "t", AccountID: "account-2", ExpiresAt: now.Add(8 * time.Hour)},
			wantWaitingA:  "c",
			wantSandboxID: "machine-1",
		},
		{
			name:          "the session's machine was recreated",
			others:        OtherAccounts{Waiting: &waiting},
			sandboxID:     "machine-2",
			offered:       waiting.Login,
			wantWaitingA:  "b",
			wantSandboxID: "machine-2",
		},
		{
			name:          "a third account waits its turn",
			others:        OtherAccounts{Waiting: &waiting},
			sandboxID:     "machine-3",
			offered:       Login{AccessToken: "d", RefreshToken: "u", AccountID: "account-3", ExpiresAt: now.Add(8 * time.Hour)},
			wantWaitingA:  "b",
			wantSandboxID: "machine-1",
		},
		{
			name:          "an older login for that account from another session",
			others:        OtherAccounts{Waiting: &waiting},
			sandboxID:     "machine-2",
			offered:       Login{AccessToken: "older", RefreshToken: "t", AccountID: "account-2", ExpiresAt: now.Add(time.Hour)},
			wantWaitingA:  "b",
			wantSandboxID: "machine-1",
		},
		{
			name:      "an account the user chose not to share",
			others:    OtherAccounts{Kept: []string{"account-2"}},
			sandboxID: "machine-1",
			offered:   waiting.Login,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			_ = store.Save("claude", shared)
			_ = store.SaveOtherAccounts("claude", tc.others)
			provider := &countingProvider{owners: map[string]string{tc.offered.AccessToken: tc.offered.AccountID}}
			keeper := NewKeeper(store, map[string]Provider{"claude": provider})

			if err := keeper.Offer(context.Background(), "claude", tc.sandboxID, tc.offered); !errors.Is(err, ErrDifferentAccount) {
				t.Fatalf("Offer err = %v, want ErrDifferentAccount", err)
			}
			if got, _, _ := store.Load("claude"); got.AccessToken != "a" {
				t.Errorf("shared access token = %q, want it unchanged", got.AccessToken)
			}
			others, _ := store.LoadOtherAccounts("claude")
			if got := waitingAccessToken(store); got != tc.wantWaitingA {
				t.Errorf("waiting access token = %q, want %q", got, tc.wantWaitingA)
			}
			if others.Waiting != nil && others.Waiting.SandboxID != tc.wantSandboxID {
				t.Errorf("waiting on machine %q, want %q", others.Waiting.SandboxID, tc.wantSandboxID)
			}
		})
	}
}

func TestKeeper_Status(t *testing.T) {
	shared := Login{AccessToken: "a", AccountID: "account-1", AccountName: "me@example.com"}
	waiting := WaitingLogin{Login: Login{AccessToken: "b", AccountID: "account-2", AccountName: "work@example.com"}, SandboxID: "machine-1"}
	cases := []struct {
		name   string
		shared *Login
		others OtherAccounts
		want   Status
	}{
		{name: "nobody logged in"},
		{name: "logged in", shared: &shared, want: Status{LoggedIn: true, Account: "me@example.com"}},
		{
			name:   "another account waiting",
			shared: &shared,
			others: OtherAccounts{Waiting: &waiting},
			want:   Status{LoggedIn: true, Account: "me@example.com", Waiting: &WaitingStatus{Account: "work@example.com", SandboxID: "machine-1"}},
		},
		{name: "another account kept", shared: &shared, others: OtherAccounts{Kept: []string{"account-2"}}, want: Status{LoggedIn: true, Account: "me@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			if tc.shared != nil {
				_ = store.Save("claude", *tc.shared)
			}
			_ = store.SaveOtherAccounts("claude", tc.others)
			keeper := NewKeeper(store, map[string]Provider{"claude": &countingProvider{}})

			got, err := keeper.Status("claude")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Status = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestKeeper_SwitchToWaitingLogin(t *testing.T) {
	shared := Login{AccessToken: "a", AccountID: "account-1"}
	waiting := WaitingLogin{Login: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2"}, SandboxID: "machine-1"}
	cases := []struct {
		name             string
		others           OtherAccounts
		refreshErr       error
		refreshedAccount string
		wantErr          bool
		wantSharedA      string
		wantWaitingA     string
	}{
		{name: "another account waiting, kept as the vendor refreshed it", others: OtherAccounts{Waiting: &waiting}, wantSharedA: "refreshed-access"},
		{name: "the waiting login refreshes into another account", others: OtherAccounts{Waiting: &waiting}, refreshedAccount: "account-9", wantErr: true, wantSharedA: "a", wantWaitingA: "b"},
		{name: "the waiting login does not refresh", others: OtherAccounts{Waiting: &waiting}, refreshErr: errors.New("invalid_grant"), wantErr: true, wantSharedA: "a", wantWaitingA: "b"},
		{name: "nothing waiting", wantErr: true, wantSharedA: "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			_ = store.Save("claude", shared)
			_ = store.SaveOtherAccounts("claude", tc.others)
			owners := map[string]string{"b": "account-2"}
			if tc.refreshedAccount != "" {
				owners["refreshed-access"] = tc.refreshedAccount
			}
			keeper := NewKeeper(store, map[string]Provider{"claude": &countingProvider{owners: owners, err: tc.refreshErr}})

			if err := keeper.SwitchToWaitingLogin(context.Background(), "claude"); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if got, _, _ := store.Load("claude"); got.AccessToken != tc.wantSharedA {
				t.Errorf("shared access token = %q, want %q", got.AccessToken, tc.wantSharedA)
			}
			if got := waitingAccessToken(store); got != tc.wantWaitingA {
				t.Errorf("waiting access token = %q, want %q", got, tc.wantWaitingA)
			}
		})
	}
}

func TestKeeper_KeepSharedLogin(t *testing.T) {
	waiting := WaitingLogin{Login: Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2"}, SandboxID: "machine-1"}
	cases := []struct {
		name     string
		others   OtherAccounts
		wantErr  error
		wantKept []string
	}{
		{name: "another account waiting", others: OtherAccounts{Waiting: &waiting}, wantKept: []string{"account-2"}},
		{name: "nothing waiting", wantErr: ErrNoWaitingLogin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			_ = store.Save("claude", Login{AccessToken: "a", AccountID: "account-1"})
			_ = store.SaveOtherAccounts("claude", tc.others)
			keeper := NewKeeper(store, map[string]Provider{"claude": &countingProvider{}})

			if err := keeper.KeepSharedLogin("claude"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			others, _ := store.LoadOtherAccounts("claude")
			if others.Waiting != nil || !slices.Equal(others.Kept, tc.wantKept) {
				t.Errorf("others = %+v, want nothing waiting and kept %v", others, tc.wantKept)
			}
			if got, _, _ := store.Load("claude"); got.AccessToken != "a" {
				t.Errorf("shared access token = %q, want it unchanged", got.AccessToken)
			}
		})
	}
}

func TestKeeper_Offer_AsksTheVendorOncePerToken(t *testing.T) {
	store := newMemoryStore()
	_ = store.Save("claude", Login{AccessToken: "a", RefreshToken: "r", AccountID: "account-1"})
	provider := &countingProvider{owners: map[string]string{"b": "account-2"}}
	keeper := NewKeeper(store, map[string]Provider{"claude": provider})

	for range 3 {
		_ = keeper.Offer(context.Background(), "claude", "machine-1", Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2"})
	}
	if got := provider.accountChecks.Load(); got != 1 {
		t.Errorf("asked the vendor %d times, want once: a waiting session offers every round", got)
	}
}

// errAnyError marks a case that wants some error, whichever it is.
var errAnyError = errors.New("any error")

func TestKeeper_Offer_WaitsBeforeTryingAFailedLoginAgain(t *testing.T) {
	now := time.Now()
	store := newMemoryStore()
	offered := Login{AccessToken: "b", RefreshToken: "made-up", AccountID: "account-1", ExpiresAt: now.Add(8 * time.Hour)}
	provider := &countingProvider{owners: map[string]string{"b": "account-1"}, err: errors.New("invalid_grant")}
	keeper := NewKeeper(store, map[string]Provider{"claude": provider})
	keeper.now = func() time.Time { return now }

	for range 3 {
		_ = keeper.Offer(context.Background(), "claude", "machine-1", offered)
	}
	if got := provider.refreshes.Load(); got != 1 {
		t.Errorf("refreshed %d times, want once: a session offers every round", got)
	}
	keeper.now = func() time.Time { return now.Add(offerRetryAfter + time.Second) }
	_ = keeper.Offer(context.Background(), "claude", "machine-1", offered)
	if got := provider.refreshes.Load(); got != 2 {
		t.Errorf("refreshed %d times after the wait, want it tried again", got)
	}
}
