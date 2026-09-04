package connect

import (
	"log/slog"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

func TestCallerIsOwner(t *testing.T) {
	person := &tailnet.Identity{UserID: 1}
	otherPerson := &tailnet.Identity{UserID: 2}
	machine := &tailnet.Identity{UserID: 3, FromTaggedDevice: true}

	cases := []struct {
		name   string
		caller *tailnet.Identity
		owner  *tailnet.Identity
		want   bool
	}{
		{name: "the owner", caller: person, owner: person, want: true},
		{name: "someone else on the tailnet", caller: otherPerson, owner: person},
		{name: "no owner yet", caller: person, owner: nil},
		{name: "a tagged caller", caller: machine, owner: person},
		{name: "a tagged node", caller: machine, owner: machine},
		{name: "an unknown user", caller: &tailnet.Identity{}, owner: &tailnet.Identity{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerIsOwner(tc.caller, tc.owner); got != tc.want {
				t.Errorf("callerIsOwner = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOwnerOrClaim(t *testing.T) {
	rollie := &tailnet.Identity{UserID: 1, LoginName: "rollie@example.com"}
	someoneElse := &tailnet.Identity{UserID: 2, LoginName: "other@example.com"}
	machine := &tailnet.Identity{UserID: 3, FromTaggedDevice: true}
	newManager := func(t *testing.T) *Manager {
		return &Manager{db: db.OpenForTest(t), logger: slog.New(slog.DiscardHandler)}
	}

	t.Run("an unowned node takes the first person who reaches it", func(t *testing.T) {
		m := newManager(t)
		got := m.ownerOrClaim("node-1", machine, rollie)
		if !callerIsOwner(rollie, got) {
			t.Fatalf("owner = %+v, want the caller", got)
		}
		if next := m.ownerOrClaim("node-1", machine, someoneElse); callerIsOwner(someoneElse, next) {
			t.Error("a second person must not take a node that already has an owner")
		}
	})

	t.Run("a tagged caller claims nothing", func(t *testing.T) {
		m := newManager(t)
		if got := m.ownerOrClaim("node-1", machine, machine); got != nil {
			t.Errorf("owner = %+v, want none from a tagged caller", got)
		}
	})

	t.Run("an owned node is left alone", func(t *testing.T) {
		m := newManager(t)
		if got := m.ownerOrClaim("node-1", rollie, someoneElse); !callerIsOwner(rollie, got) {
			t.Errorf("owner = %+v, want the existing owner", got)
		}
	})
}
