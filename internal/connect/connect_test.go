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
	m := NewManager(context.Background(), t.TempDir(), nil, nil)

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
