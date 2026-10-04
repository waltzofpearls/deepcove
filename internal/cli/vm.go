package cli

import (
	"context"
	"fmt"
	"os"
)

// cmdVM is the family of VM commands. Only `run` exists today, and it is deepcove's own: one VM per
// process, started by deepcove, never typed by hand.
func cmdVM(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "run" {
		return fmt.Errorf("usage: deepcove vm run <file>")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	return runVM(ctx, data)
}
