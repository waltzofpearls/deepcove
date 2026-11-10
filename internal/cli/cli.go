// Package cli is deepcove's command dispatcher: stdlib flag only, file-per-command pattern.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/waltzofpearls/deepcove/internal/session"
)

// command is one top-level subcommand. Each lives in its own file (start.go, status.go, session.go,
// and so on) and registers itself here.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string) error
}

var commands = []command{
	{"start", "run deepcove in the foreground", cmdStart},
	{"service", "install deepcove as a background service", cmdService},
	{"status", "is deepcove running, and where to reach it", cmdStatus},
	{"open", "open deepcove in your browser", cmdOpen},
	{"connect", "connect to a session", cmdSessionConnect},
	{"ls", "list sessions", cmdSessionList},
	{"session", "create or attach to a session", cmdSession},
	{"network", "put this machine on your Tailscale network", cmdNetwork},
	{"upgrade", "replace this binary with the newest release", cmdUpgrade},
	{"config", "settings this node keeps", cmdConfig},
	{"db", "check or repair the local database", cmdDB},
	{"version", "print version", cmdVersion},
}

// internalCommands are run by deepcove itself, never typed, so the help leaves them out.
var internalCommands = []command{
	{"vm", "", cmdVM},
}

// Run dispatches to a subcommand and returns an exit code. Intended to be called from cmd/deepcove/main.go as:
//
//	os.Exit(cli.Run(os.Args[1:]))
func Run(args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return 2
	}

	name, rest := args[0], args[1:]

	switch name {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	}

	run, rest, found := findCommand(name, rest)
	if !found {
		_, _ = fmt.Fprintf(os.Stderr, "deepcove: unknown command %q\n\n", name)
		usage(os.Stderr)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "deepcove:", err)
		return 1
	}
	return 0
}

// findCommand finds what runs name. An agent's name creates a session, as `deepcove session <agent>` does.
func findCommand(name string, rest []string) (func(context.Context, []string) error, []string, bool) {
	for _, cmd := range slices.Concat(commands, internalCommands) {
		if cmd.name == name {
			return cmd.run, rest, true
		}
	}
	if session.IsKnownAgent(name) {
		return cmdSessionCreate, append([]string{name}, rest...), true
	}
	return nil, nil, false
}

func usage(w *os.File) {
	lines := []string{
		"deepcove: a self-hosted, isolated coding agent studio on your tailnet.",
		"",
		"Usage:",
		"  deepcove <command> [flags] [args]",
		"",
		"Commands:",
	}
	for _, c := range commands {
		lines = append(lines, fmt.Sprintf("  %-10s  %s", c.name, c.summary))
	}
	lines = append(lines, fmt.Sprintf("  %-10s  %s", "<agent>", "create a session, e.g. deepcove claude"))
	lines = append(lines, "", "Run 'deepcove <command> -h' for command-specific help.")
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}

func joinStringLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
