//go:build !darwin

package cli

import (
	"log/slog"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
)

// newVMRuntime has nothing to build off macOS: Apple VF is macOS only, and Firecracker (Linux) is not
// built yet. Such a node runs sessions in containers until one of those lands.
func newVMRuntime(*config.Config, []string, credbroker.Store, *slog.Logger) runtimeEntry {
	return runtimeEntry{}
}
