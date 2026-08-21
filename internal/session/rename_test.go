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

	t.Run("rejects bad names", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		sess, _ := svc.Create(ctx, CreateRequest{Agent: "claude"})
		for _, name := range badSessionNames(t) {
			if _, err := svc.Rename(ctx, sess.ID, name); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("rename %q: err = %v, want ErrInvalidInput", name, err)
			}
		}
	})

	t.Run("unknown id is ErrNotFound", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		if _, err := svc.Rename(ctx, "01900000-0000-7000-8000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

// badSessionNames lists names no session may have. A name becomes a folder on the shared machine, so
// anything that would step outside it is rejected.
func badSessionNames(t *testing.T) []string {
	t.Helper()
	return []string{"", "   ", strings.Repeat("x", maxSessionNameLen+1), "/etc", "/", "../evil", "a/../b", "..", ".", "a/./b"}
}

func TestValidateSessionName(t *testing.T) {
	cases := []struct {
		name string
		want string // empty means the name is rejected
	}{
		{"my-session", "my-session"},
		{"  trimmed  ", "trimmed"},
		{"org/repo", "org/repo"},
		{"a/b/c", "a/b/c"},
		{"..dots", "..dots"},
		{"a..b", "a..b"},
		{strings.Repeat("x", maxSessionNameLen), strings.Repeat("x", maxSessionNameLen)},
	}
	for _, bad := range badSessionNames(t) {
		cases = append(cases, struct {
			name string
			want string
		}{bad, ""})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateSessionName(tc.name)
			if tc.want == "" {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("err = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCreateRejectsABadName(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	_, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "../evil"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if n := len(fake.created); n != 0 {
		t.Errorf("built %d sandboxes, want none", n)
	}
}
