// Package cli is deepcove's command dispatcher: stdlib flag only, file-per-command pattern.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// command is one top-level subcommand. Each lives in its own file (start.go, status.go, session.go, ...)
// and registers itself here.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string) error
}

var commands = []command{
	{"start", "run the background service", cmdStart},
	{"dashboard", "open the dashboard", cmdDashboard},
	{"session", "create or attach to a session", cmdSession},
	{"version", "print version", cmdVersion},
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

	for _, c := range commands {
		if c.name != name {
			continue
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := c.run(ctx, rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			fmt.Fprintln(os.Stderr, "deepcove:", err)
			return 1
		}
		return 0
	}

	_, _ = fmt.Fprintf(os.Stderr, "deepcove: unknown command %q\n\n", name)
	usage(os.Stderr)
	return 2
}

func usage(w *os.File) {
	lines := []string{
		"deepcove — a self-hosted, isolated coding agent studio on your tailnet.",
		"",
		"Usage:",
		"  deepcove <command> [flags] [args]",
		"",
		"Commands:",
	}
	for _, c := range commands {
		lines = append(lines, fmt.Sprintf("  %-10s  %s", c.name, c.summary))
	}
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
