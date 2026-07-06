package connect

import (
	"context"
	"testing"
)

// TestEnabledMarker pins the auto-start marker plumbing the wizard relies on: a fresh manager is not
// enabled, markEnabled persists it, and clearEnabled removes it. The marker is what AutoStartIfEnabled
// reads on boot, and (post-fix) it's only written once a connection actually reaches Running, so an
// abandoned sign-in leaves nothing behind to auto-start a half-connected, log-spamming node.
func TestEnabledMarker(t *testing.T) {
	m := NewManager(context.Background(), t.TempDir(), nil, nil, nil)

	if m.isEnabled() {
		t.Fatal("a fresh manager must not be enabled")
	}
	if err := m.markEnabled(); err != nil {
		t.Fatalf("markEnabled: %v", err)
	}
	if !m.isEnabled() {
		t.Error("expected enabled after markEnabled")
	}
	m.clearEnabled()
	if m.isEnabled() {
		t.Error("expected not enabled after clearEnabled")
	}
}

func TestClusterCache(t *testing.T) {
	m := NewManager(context.Background(), t.TempDir(), nil, nil, nil)

	if got := m.cachedCluster("n1"); got != "" {
		t.Errorf("empty cache -> %q, want empty", got)
	}
	m.cacheCluster("n1", "dev")
	if got := m.cachedCluster("n1"); got != "dev" {
		t.Errorf("cachedCluster = %q, want dev", got)
	}
	m.cacheCluster("n1", "")
	if got := m.cachedCluster("n1"); got != "dev" {
		t.Errorf("empty label clobbered cache: %q", got)
	}
	m.cacheCluster("", "prod")
	if got := m.cachedCluster(""); got != "" {
		t.Errorf("empty id was cached: %q", got)
	}
	m.cacheCluster("n1", "prod")
	if got := m.cachedCluster("n1"); got != "prod" {
		t.Errorf("update -> %q, want prod", got)
	}
}
