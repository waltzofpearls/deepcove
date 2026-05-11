package cli

import (
	"context"
	"flag"
	"fmt"
)

// cmdSessionAttach handles `deepcove session attach <id-or-name>`. Connects
// to the local background service over the Unix socket, opens a WebSocket
// to the session's PTY endpoint, and pipes stdin/stdout. The tmux session
// inside the container stays alive across disconnects (see SPEC.md §5.10).
func cmdSessionAttach(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("session attach", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: deepcove session attach <id-or-name>")
	}
	idOrName := rest[0]

	// TODO: dial local Unix socket, resolve id-or-name → session, open
	// WebSocket to /api/sessions/<id>/pty, put terminal in raw mode, pipe
	// stdin ↔ WS ↔ stdout, send resize events as WS control frames.

	_ = ctx

	return fmt.Errorf("deepcove session attach %s: not implemented yet", idOrName)
}
