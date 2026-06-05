package session

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestService_Rename(t *testing.T) {
	ctx := context.Background()

	t.Run("trims and updates", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		sess, err := svc.Create(ctx, CreateRequest{Agent: "claude"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := svc.Rename(ctx, sess.ID, "  my-renamed  ")
		if err != nil {
			t.Fatalf("rename: %v", err)
		}
		if got.Name != "my-renamed" {
			t.Errorf("name = %q, want %q", got.Name, "my-renamed")
		}
		// Reload to confirm it persisted, not just echoed back.
		reloaded, err := svc.Get(ctx, sess.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if reloaded.Name != "my-renamed" {
			t.Errorf("persisted name = %q, want %q", reloaded.Name, "my-renamed")
		}
	})

	t.Run("rejects empty name", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		sess, _ := svc.Create(ctx, CreateRequest{Agent: "claude"})
		if _, err := svc.Rename(ctx, sess.ID, "   "); err == nil {
			t.Error("want error for blank name, got nil")
		}
	})

	t.Run("rejects over-long name", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		sess, _ := svc.Create(ctx, CreateRequest{Agent: "claude"})
		if _, err := svc.Rename(ctx, sess.ID, strings.Repeat("x", maxSessionNameLen+1)); err == nil {
			t.Error("want error for over-long name, got nil")
		}
	})

	t.Run("unknown id is ErrNotFound", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		if _, err := svc.Rename(ctx, "01900000-0000-7000-8000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}
