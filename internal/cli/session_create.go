package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// cmdSessionCreate handles `deepcove session <agent>` with optional --name and repeatable --mount
// flags. Creates a session on the local background service via the API, printing the id + name.
// Attach is a separate command (TODO).
//
// If --name is omitted, the service auto-generates one in the form "<agent>-<adjective>_<scientist>"
// (Docker-style).
//
// --mount may be repeated. Mode defaults to rw; append :ro for read-only. Container_path collisions
// with /workspace (the scratch dir) are rejected server-side.
func cmdSessionCreate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("agent name required (e.g. `deepcove session claude`)")
	}
	agent := args[0]

	fs := flag.NewFlagSet("session "+agent, flag.ContinueOnError)
	name := fs.String("name", "", "session name (auto-generated if omitted)")
	image := fs.String("image", "", "container image override (default: agent's registered image)")
	setupScript := fs.String("setup-script", "", "shell script run on container start (default: agent's registered setup)")
	mounts := &mountFlag{}
	fs.Var(mounts, "mount", "mount host:container[:ro]; repeatable")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newSessionClient(cfg)
	sess, err := client.Create(ctx, session.CreateRequest{
		Agent:       agent,
		Name:        *name,
		Image:       *image,
		SetupScript: *setupScript,
		Mounts:      mounts.values,
	})
	if err != nil {
		return err
	}

	fmt.Printf("%s  %s  %s\n", short(sess.ID), sess.Name, sess.Status)
	return nil
}

// mountFlag implements flag.Value for the repeatable --mount flag. Format: HOST:CONTAINER[:ro].
// We accept rw as an explicit third segment too so users can be defensive, but it's the default.
//
// Splitting on ':' is unambiguous because absolute paths can't contain a colon on macOS/Linux,
// the only OSes deepcove targets.
type mountFlag struct {
	values []session.MountRequest
}

func (m *mountFlag) String() string { return fmt.Sprintf("%v", m.values) }

func (m *mountFlag) Set(s string) error {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("mount %q: expected HOST:CONTAINER[:ro]", s)
	}
	mr := session.MountRequest{HostPath: parts[0], ContainerPath: parts[1]}
	if len(parts) == 3 {
		mr.Mode = parts[2]
	}
	m.values = append(m.values, mr)
	return nil
}

// short trims a UUID to the leading 12 characters, Docker-style, for terminal friendly output.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
