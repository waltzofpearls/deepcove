package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// cmdSession is the router for `deepcove session ...`.
//
// Usage:
//
//	deepcove session <agent> [--name <name>] [flags]   # create + attach
//	deepcove session attach <id-or-name>               # attach existing
//	deepcove session ps                                # list sessions
//	deepcove session help                              # subcommand usage
//
// Reserved verbs (`attach`, `ps`, `help`, `-h`, `--help`) are intercepted here; anything else is
// treated as the agent name positional and dispatched to cmdSessionCreate.
func cmdSession(ctx context.Context, args []string) error {
	if len(args) == 0 {
		sessionUsage(os.Stderr)
		return errors.New("agent name required (e.g. `deepcove session claude`)")
	}

	switch args[0] {
	case "-h", "--help", "help":
		sessionUsage(os.Stdout)
		return nil
	case "attach":
		return cmdSessionAttach(ctx, args[1:])
	case "ps":
		return cmdSessionPs(ctx, args[1:])
	}

	// First arg is the agent name; pass everything through to create.
	return cmdSessionCreate(ctx, args)
}

func sessionUsage(w *os.File) {
	lines := []string{
		"deepcove session: create, list, and attach to coding-agent sessions",
		"",
		"Usage:",
		"  deepcove session <agent> [--name <name>] [flags]   create + attach",
		"  deepcove session attach <id-or-name>               attach existing",
		"  deepcove session ps                                list sessions",
		"",
		"Agents: claude, codex, gemini, openclaw, ...",
		"",
		"Examples:",
		"  deepcove session claude                                       # create + attach a claude session, auto-named",
		"  deepcove session claude --name auth-redesign                  # create + attach with a name",
		"  deepcove session claude --mount ~/code/myapp:/workspace       # mount a host path into the sandbox (rw)",
		"  deepcove session claude --mount ~/data:/seed:ro --mount ...   # repeat for multiple mounts; :ro for read-only",
		"  deepcove session attach auth-redesign                         # attach by name",
		"  deepcove session ps                                           # list sessions on this host",
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}
