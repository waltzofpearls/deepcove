package cli

import (
	"context"
	"flag"
	"fmt"
)

// cmdSessionCreate handles `deepcove session <agent> [--name <name>] [flags]`.
// Creates a session on the local background service via the Unix socket,
// then attaches to it (see SPEC.md §5.10 for the attach mechanism).
//
// If --name is omitted, the service auto-generates one in the form
// "<agent>-<adjective>_<scientist>" (Docker-style).
func cmdSessionCreate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("agent name required (e.g. `deepcove session claude`)")
	}
	agent := args[0]

	fs := flag.NewFlagSet("session "+agent, flag.ContinueOnError)
	name := fs.String("name", "", "session name (auto-generated if omitted)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	// TODO: dial local Unix socket, POST /api/sessions with {agent, name, ...},
	// receive session id, then attach by piping stdin/stdout to the
	// /api/sessions/<id>/pty WebSocket. Same mechanism as cmdSessionAttach.

	_ = ctx
	_ = name

	return fmt.Errorf("deepcove session %s: not implemented yet", agent)
}
