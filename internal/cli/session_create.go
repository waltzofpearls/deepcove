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
// Attaching is its own command, `deepcove session attach`.
//
// If --name is omitted, the service auto-generates one in the form "<agent>-<adjective>_<scientist>".
//
// --mount may be repeated. Mode defaults to rw; append :ro for read-only. The server refuses / and
// anything under /root, the agent's home.
func cmdSessionCreate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("agent name required (e.g. `deepcove session claude`)")
	}
	agent := args[0]

	fs := flag.NewFlagSet("session "+agent, flag.ContinueOnError)
	var name, image, setupScript, runtime, workDir string
	fs.StringVar(&name, "name", "", "session name (auto-generated if omitted)")
	fs.StringVar(&runtime, "runtime", "", "runtime to host the session: applevf, cloudhv or sandboxless (default: DEEPCOVE_DEFAULT_RUNTIME if the service sets it, else this node's VM runtime; required if neither is available)")
	fs.StringVar(&workDir, "work-dir", "", "folder the agent works in, for a runtime that works in one")
	fs.StringVar(&image, "image", "", "image override (default: agent's registered image)")
	fs.StringVar(&setupScript, "setup-script", "", "shell script run on session start (default: agent's registered setup)")
	mounts := &mountFlag{}
	fs.Var(mounts, "mount", "mount host:container[:ro]; repeatable")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newAPIClient(cfg)
	sess, err := client.Create(ctx, session.CreateRequest{
		Agent:       agent,
		Name:        name,
		Runtime:     runtime,
		WorkDir:     workDir,
		Image:       image,
		SetupScript: setupScript,
		Mounts:      mounts.values,
	})
	if err != nil {
		return err
	}

	fmt.Printf("%s  %s  %s  %s\n", short(sess.ID), sess.Name, sess.Runtime, sess.Status)
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

// short trims a UUID to the leading 12 characters, for terminal friendly output.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
