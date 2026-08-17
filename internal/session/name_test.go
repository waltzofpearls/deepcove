package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGenerateName(t *testing.T) {
	name := generateName("claude")
	agent, rest, ok := strings.Cut(name, "-")
	if !ok || agent != "claude" {
		t.Fatalf("name %q, want it to start with the agent", name)
	}
	if _, _, ok := strings.Cut(rest, "_"); !ok {
		t.Errorf("name %q, want <adjective>_<scientist> after the agent", name)
	}
}

func TestGenerateUniqueNameAvoidsTakenNames(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	taken := map[string]bool{}
	for i, name := range []string{
		"claude-witty_curie", "claude-bold_darwin", "claude-calm_einstein",
		"claude-eager_feynman", "claude-fierce_galileo",
	} {
		taken[name] = true
		err := svc.repo.Insert(ctx, &Session{
			ID:             fmt.Sprintf("019ff000-0000-7000-8000-%012d", i),
			Name:           name,
			Agent:          "claude",
			Image:          "alpine:3.20",
			OwnerEmail:     placeholderOwner,
			Status:         StatusStopped,
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		})
		if err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}

	// Repeated, because one call landing on a free name proves nothing: the generator is random.
	for range 50 {
		if got := svc.generateUniqueName(ctx, "claude"); taken[got] {
			t.Fatalf("got %q, which is already in use", got)
		}
	}
}
