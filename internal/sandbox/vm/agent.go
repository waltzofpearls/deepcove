package vm

import (
	"context"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

var _ sandbox.AgentProvisioner = (*Driver)(nil)

// Setup reads how far the entrypoint got, from the files it wrote inside the guest.
func (d *Driver) Setup(ctx context.Context, sandboxID sandbox.ID) (sandbox.SetupState, error) {
	return sandbox.SandboxedSetup(ctx, d, sandboxID)
}
