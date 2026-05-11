package cli

import (
	"context"
	"flag"
	"fmt"
)

// cmdSessionPs handles `deepcove session ps`. Lists sessions on the local
// background service, Docker-style. Output columns: ID, NAME, AGENT,
// STATUS, AGE.
func cmdSessionPs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("session ps", flag.ContinueOnError)
	all := fs.Bool("a", false, "show all sessions (default: only running)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// TODO: dial local Unix socket, GET /api/sessions, render as a table.

	_ = ctx
	_ = all

	return fmt.Errorf("deepcove session ps: not implemented yet")
}
