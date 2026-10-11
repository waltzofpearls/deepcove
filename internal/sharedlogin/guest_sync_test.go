package sharedlogin

import "testing"

// fakeFiles is an agent's login files in memory.
type fakeFiles struct {
	local         Login
	onboarded     bool
	tokenWrites   []Login
	accountWrites []Login
	removals      int
}

func (f *fakeFiles) TokenPath() string { return "/root/.agent/tokens" }

func (f *fakeFiles) RemoveTokens() error {
	f.removals++
	f.local.AccessToken, f.local.RefreshToken = "", ""
	return nil
}

func (f *fakeFiles) Read() (Login, bool, error) { return f.local, f.onboarded, nil }

func (f *fakeFiles) WriteTokens(login Login) error {
	f.tokenWrites = append(f.tokenWrites, login)
	f.local.AccessToken, f.local.RefreshToken = login.AccessToken, login.RefreshToken
	return nil
}

func (f *fakeFiles) WriteAccount(login Login) error {
	f.accountWrites = append(f.accountWrites, login)
	f.local.AccountID, f.onboarded = login.AccountID, true
	return nil
}

func TestGuestSync_Once(t *testing.T) {
	shared := Login{AccessToken: "shared", AccountID: "account-1"}
	cases := []struct {
		name              string
		local             Login
		onboarded         bool
		reply             Reply
		wantOffer         bool
		wantTokenWrites   int
		wantAccountWrites int
		wantRemovals      int
	}{
		{name: "a fresh session gets the shared login", reply: Reply{Login: &shared}, wantTokenWrites: 1, wantAccountWrites: 1},
		{name: "already up to date", local: shared, onboarded: true, reply: Reply{Login: &shared}},
		{name: "a newer shared token", local: Login{AccessToken: "old", AccountID: "account-1"}, onboarded: true, reply: Reply{Login: &shared}, wantTokenWrites: 1},
		{name: "the session's own login is offered, then kept without its refresh token", local: Login{AccessToken: "shared", RefreshToken: "own", AccountID: "account-1"}, onboarded: true, reply: Reply{Login: &shared}, wantOffer: true, wantTokenWrites: 1},
		{name: "another account is kept as it is", local: Login{AccessToken: "own", RefreshToken: "own", AccountID: "account-2"}, onboarded: true, reply: Reply{DifferentAccount: true}, wantOffer: true},
		{name: "nobody has logged in yet", reply: Reply{NoLogin: true}},
		{name: "sharing turned off drops the shared copy", local: shared, onboarded: true, reply: Reply{NotSharing: true}, wantRemovals: 1},
		{name: "sharing off keeps the session's own login", local: Login{AccessToken: "own", RefreshToken: "own", AccountID: "account-2"}, onboarded: true, reply: Reply{NotSharing: true}, wantOffer: true},
		{name: "sharing off with no login", reply: Reply{NotSharing: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := &fakeFiles{local: tc.local, onboarded: tc.onboarded}
			var offered bool
			sync := &GuestSync{Files: files, Ask: func(request Request) (Reply, error) {
				offered = request.Offer != nil
				return tc.reply, nil
			}}

			if err := sync.Once(); err != nil {
				t.Fatalf("Once: %v", err)
			}
			if offered != tc.wantOffer {
				t.Errorf("offered = %v, want %v", offered, tc.wantOffer)
			}
			if len(files.tokenWrites) != tc.wantTokenWrites || len(files.accountWrites) != tc.wantAccountWrites {
				t.Errorf("wrote tokens %d and account %d times, want %d and %d",
					len(files.tokenWrites), len(files.accountWrites), tc.wantTokenWrites, tc.wantAccountWrites)
			}
			if files.removals != tc.wantRemovals {
				t.Errorf("removed tokens %d times, want %d", files.removals, tc.wantRemovals)
			}
			for _, written := range files.tokenWrites {
				if written.RefreshToken != "" {
					t.Error("wrote a refresh token into the session")
				}
			}
		})
	}
}

func TestGuestSync_Once_ReportsAHostError(t *testing.T) {
	sync := &GuestSync{Files: &fakeFiles{}, Ask: func(Request) (Reply, error) {
		return Reply{Error: "refresh failed"}, nil
	}}
	if err := sync.Once(); err == nil || err.Error() != "refresh failed" {
		t.Errorf("Once err = %v, want the host's error", err)
	}
}
