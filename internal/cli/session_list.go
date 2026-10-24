package cli

import (
	"context"
	"flag"
	"fmt"
	"text/tabwriter"
	"time"

	"os"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// cmdSessionList handles `deepcove session list` and `deepcove session ls`. Lists sessions on all your
// nodes, running ones only unless -a. Output columns: ID, NAME, NODE, AGENT, RUNTIME, STATUS, AGE.
func cmdSessionList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	var all bool
	fs.BoolVar(&all, "a", false, "show all sessions (default: only running)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newAPIClient(cfg)
	listed, err := client.ListEveryNode(ctx, all)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	lines := []string{"ID\tNAME\tNODE\tAGENT\tRUNTIME\tSTATUS\tAGE"}
	for _, s := range listed.Sessions {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s",
			short(s.ID), s.Name, nodeName(s), s.Agent, s.Runtime, s.Status, ageOf(s.CreatedAt)))
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
	_ = w.Flush()
	for _, node := range listed.Unreachable {
		_, _ = fmt.Fprintf(os.Stderr, "%s did not answer, so its sessions are missing\n", node.Hostname)
	}
	return nil
}

// nodeName is the node hosting a session. A node that is not on the tailnet tags nothing, and every
// session it lists is its own.
func nodeName(s *session.Session) string {
	if s.Host == nil {
		return "this node"
	}
	return s.Host.Hostname
}

// ageOf formats a timestamp as a coarse "5m" / "2h" / "3d" string suitable for table output. Doesn't
// try to be precise; the unit reflects the largest non-zero bucket.
func ageOf(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
