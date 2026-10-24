package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// cmdSessionCreate handles `deepcove session <agent>` with optional --name and repeatable --share
// flags. Creates a session on the local background service via the API, printing the id + name.
// Connecting is its own command, `deepcove session connect`.
//
// If --name is omitted, the service auto-generates one in the form "<agent>-<adjective>_<scientist>".
//
// --share may be repeated. A folder is read and write unless :ro is appended. The server refuses / and
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
	sharedFolders := &sharedFolderFlag{}
	fs.Var(sharedFolders, "share", "share a host folder as host:guest[:ro]; repeatable")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newAPIClient(cfg)
	sess, err := client.Create(ctx, session.CreateRequest{
		Agent:         agent,
		Name:          name,
		Runtime:       runtime,
		WorkDir:       workDir,
		Image:         image,
		SetupScript:   setupScript,
		SharedFolders: sharedFolders.values,
	})
	if err != nil {
		return err
	}

	fmt.Printf("%s  %s  %s  %s\n", short(sess.ID), sess.Name, sess.Runtime, sess.Status)
	return nil
}

// sharedFolderFlag implements flag.Value for the repeatable --share flag. Format: HOST:GUEST[:ro|:rw].
// rw is the default, and is accepted as a third segment for users who want to say so.
//
// Splitting on ':' is unambiguous because absolute paths can't contain a colon on macOS/Linux,
// the only OSes deepcove targets.
type sharedFolderFlag struct {
	values []session.SharedFolderRequest
}

func (f *sharedFolderFlag) String() string { return fmt.Sprintf("%v", f.values) }

func (f *sharedFolderFlag) Set(value string) error {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("share %q: expected HOST:GUEST[:ro]", value)
	}
	folder := session.SharedFolderRequest{HostPath: parts[0], GuestPath: parts[1]}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			folder.ReadOnly = true
		case "rw":
		default:
			return fmt.Errorf("share %q: the third part must be ro or rw", value)
		}
	}
	f.values = append(f.values, folder)
	return nil
}

// short trims a UUID to the leading 12 characters, for terminal friendly output.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
