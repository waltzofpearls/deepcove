package sharedlogin

import (
	"net"
	"testing"
	"time"
)

func TestServe(t *testing.T) {
	shared := Login{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(4 * time.Hour), AccountID: "account-1"}
	cases := []struct {
		name       string
		notSharing bool
		stored     *Login
		request    Request
		wantReply  Reply
		wantAccess string
	}{
		{name: "asking before anyone logged in", request: Request{Agent: "claude"}, wantReply: Reply{NoLogin: true}},
		{name: "asking for the shared login", stored: &shared, request: Request{Agent: "claude"}, wantAccess: "a"},
		{
			name:       "offering the first login",
			request:    Request{Agent: "claude", Offer: &Login{AccessToken: "b", RefreshToken: "s", ExpiresAt: time.Now().Add(8 * time.Hour), AccountID: "account-1"}},
			wantAccess: "refreshed-access",
		},
		{
			name:      "offering another account",
			stored:    &shared,
			request:   Request{Agent: "claude", Offer: &Login{AccessToken: "b", RefreshToken: "s", AccountID: "account-2"}},
			wantReply: Reply{DifferentAccount: true},
		},
		{name: "a session that does not share", notSharing: true, stored: &shared, request: Request{Agent: "claude"}, wantReply: Reply{NotSharing: true}},
		{name: "asking for an agent this machine does not run", stored: &shared, request: Request{Agent: "codex"}, wantReply: Reply{Error: "this machine gets no shared login for \"codex\""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryStore()
			if tc.stored != nil {
				_ = store.Save("claude", *tc.stored)
			}
			provider := &countingProvider{}
			if offer := tc.request.Offer; offer != nil {
				provider.owners = map[string]string{offer.AccessToken: offer.AccountID}
			}
			keeper := NewKeeper(store, map[string]Provider{"claude": provider})
			host, guest := net.Pipe()
			keeper.SetSharingCheck(func(agent, sessionID string) (bool, error) {
				return agent == "claude" && sessionID == "session-1" && !tc.notSharing, nil
			})
			go func() { _ = Serve(host, keeper, []string{"claude"}, "session-1", "machine-1") }()

			reply, err := Ask(guest, tc.request)
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			if tc.wantAccess == "" {
				if reply.NoLogin != tc.wantReply.NoLogin || reply.DifferentAccount != tc.wantReply.DifferentAccount || reply.NotSharing != tc.wantReply.NotSharing || reply.Error != tc.wantReply.Error || reply.Login != nil {
					t.Errorf("reply = %+v, want %+v", reply, tc.wantReply)
				}
				return
			}
			if reply.Login == nil || reply.Login.AccessToken != tc.wantAccess || reply.Login.RefreshToken != "" {
				t.Errorf("reply = %+v, want login %q with no refresh token", reply, tc.wantAccess)
			}
		})
	}
}
