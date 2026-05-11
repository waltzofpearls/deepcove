package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// cmdSession is the router for `deepcove session ...`. See SPEC.md §8.
//
// Shape:
//
//	deepcove session <agent> [--name <name>] [flags]   # create + attach
//	deepcove session attach <id-or-name>               # attach existing
//	deepcove session ps                                # list sessions
//	deepcove session help                              # subcommand usage
//
// Reserved verbs (`attach`, `ps`, `help`, `-h`, `--help`) are intercepted
// here; anything else is treated as the agent name positional and
// dispatched to cmdSessionCreate.
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
	fmt.Fprintln(w, "deepcove session — create, list, and attach to coding-agent sessions")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  deepcove session <agent> [--name <name>] [flags]   create + attach")
	fmt.Fprintln(w, "  deepcove session attach <id-or-name>               attach existing")
	fmt.Fprintln(w, "  deepcove session ps                                list sessions")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Agents: claude, codex, gemini, openclaw, ...")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  deepcove session claude                       # create + attach a claude session, auto-named")
	fmt.Fprintln(w, "  deepcove session claude --name auth-redesign  # create + attach with a name")
	fmt.Fprintln(w, "  deepcove session attach auth-redesign         # attach by name")
	fmt.Fprintln(w, "  deepcove session ps                           # list sessions on this host")
}
