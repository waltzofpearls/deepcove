package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

func TestSharedLoginStore_RoundTrip(t *testing.T) {
	store := NewSharedLoginStore(db.OpenForTest(t))

	if _, ok, err := store.Load("claude"); err != nil || ok {
		t.Fatalf("Load before any save = ok %v, err %v; want none", ok, err)
	}
	saved := sharedlogin.Login{
		AccessToken:  "a",
		RefreshToken: "r",
		ExpiresAt:    time.UnixMilli(1791000000000),
		AccountID:    "account-1",
	}
	if err := store.Save("claude", saved); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, ok, err := store.Load("claude")
	if err != nil || !ok {
		t.Fatalf("Load = ok %v, err %v", ok, err)
	}
	if loaded.AccessToken != "a" || loaded.RefreshToken != "r" || loaded.AccountID != "account-1" || !loaded.ExpiresAt.Equal(saved.ExpiresAt) {
		t.Errorf("loaded %+v, want %+v", loaded, saved)
	}
}

func TestSharedLoginStore_OtherAccountsRoundTrip(t *testing.T) {
	store := NewSharedLoginStore(db.OpenForTest(t))

	if others, err := store.LoadOtherAccounts("claude"); err != nil || others.Waiting != nil || others.Kept != nil {
		t.Fatalf("LoadOtherAccounts before any save = %+v, %v; want empty", others, err)
	}
	saved := sharedlogin.OtherAccounts{
		Waiting: &sharedlogin.WaitingLogin{Login: sharedlogin.Login{AccessToken: "b", AccountID: "account-2"}, SandboxID: "machine-1"},
		Kept:    []string{"account-3"},
	}
	if err := store.SaveOtherAccounts("claude", saved); err != nil {
		t.Fatalf("SaveOtherAccounts: %v", err)
	}
	loaded, err := store.LoadOtherAccounts("claude")
	if err != nil {
		t.Fatalf("LoadOtherAccounts: %v", err)
	}
	if !reflect.DeepEqual(loaded, saved) {
		t.Errorf("loaded %+v, want %+v", loaded, saved)
	}
}
