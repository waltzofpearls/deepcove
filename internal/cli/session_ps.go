package cli

import (
	"context"
	"flag"
	"fmt"
	"text/tabwriter"
	"time"

	"os"

	"github.com/waltzofpearls/deepcove/internal/config"
)

// cmdSessionPs handles `deepcove session ps`. Lists sessions on the local background service,
// Docker-style. Output columns: ID, NAME, AGENT, STATUS, AGE.
func cmdSessionPs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("session ps", flag.ContinueOnError)
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
	sessions, err := client.List(ctx, all)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	lines := []string{"ID\tNAME\tAGENT\tSTATUS\tAGE"}
	for _, s := range sessions {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			short(s.ID), s.Name, s.Agent, s.Status, ageOf(s.CreatedAt)))
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
	_ = w.Flush()
	return nil
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
