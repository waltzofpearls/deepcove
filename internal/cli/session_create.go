package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// cmdSessionCreate handles `deepcove session <agent> [--name <name>] [flags]`.
// Creates a session on the local background service via the API,
// printing the id + name. Attach is a separate command (TODO).
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

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newSessionClient(cfg)
	sess, err := client.Create(ctx, session.CreateRequest{
		Agent: agent,
		Name:  *name,
	})
	if err != nil {
		return err
	}

	fmt.Printf("%s  %s  %s\n", short(sess.ID), sess.Name, sess.Status)
	return nil
}

// short trims a UUID to the leading 12 characters, Docker-style, for
// terminal-friendly output.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
