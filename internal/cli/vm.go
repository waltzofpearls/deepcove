package cli

import (
	"context"
	"fmt"
	"os"
)

// cmdVM is the family of VM commands. They are deepcove's own, started by deepcove, never typed by hand:
// `run` hosts one Mac VM per process, and `guard-network` sets up the network a Linux VM's passt runs in.
func cmdVM(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "guard-network" {
		return guardNetwork(args[1:])
	}
	if len(args) != 2 || args[0] != "run" {
		return fmt.Errorf("usage: deepcove vm run <file>")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	return runVM(ctx, data)
}
