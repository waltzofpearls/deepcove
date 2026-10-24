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
//	deepcove session connect <id-or-name>              # connect to an existing session
//	deepcove session list                              # list sessions, ls works too
//	deepcove session help                              # subcommand usage
//
// Reserved verbs (`connect`, `list`, `ls`, `help`, `-h`, `--help`) are intercepted here; anything else is
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
	case "connect":
		return cmdSessionConnect(ctx, args[1:])
	case "list", "ls":
		return cmdSessionList(ctx, args[1:])
	}

	// First arg is the agent name; pass everything through to create.
	return cmdSessionCreate(ctx, args)
}

func sessionUsage(w *os.File) {
	lines := []string{
		"deepcove session: create, list, and connect to coding-agent sessions",
		"",
		"Usage:",
		"  deepcove session <agent> [--name <name>] [flags]   create + attach",
		"  deepcove session connect <id-or-name>              connect to an existing session",
		"  deepcove session list                              list sessions (or ls)",
		"",
		"Agents: claude, codex, gemini, openclaw, ...",
		"",
		"Examples:",
		"  deepcove session claude                                       # create + attach a claude session, auto-named",
		"  deepcove session claude --name auth-redesign                  # create + attach with a name",
		"  deepcove session claude --share ~/code/myapp:/workspace       # share a host folder with the sandbox (rw)",
		"  deepcove session claude --share ~/data:/seed:ro --share ...   # repeat for several folders; :ro for read-only",
		"  deepcove session connect auth-redesign                        # connect by name",
		"  deepcove session ls                                           # list sessions on this host",
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}
