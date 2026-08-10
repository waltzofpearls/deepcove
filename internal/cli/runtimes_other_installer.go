//go:build !darwin

package cli

import (
	"log/slog"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// registerVMInstaller is a no-op off macOS: there is no VM runtime here to set up, so the UI is never
// offered a button for one.
func registerVMInstaller(*session.Service, *config.Config, []string, credbroker.Store, *slog.Logger, *runtimeSet) {
}
