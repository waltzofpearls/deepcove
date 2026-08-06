package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// labelManaged marks a container as one of ours. Docker hosts plenty that are not, so without it a
// listing would sweep in every unrelated container on the machine and call it a deepcove sandbox.
//
// Containers created before this label existed are invisible to ListSandboxes. Recreating the session
// fixes it, and the alternative (guessing from image or name) would mislabel someone else's container,
// which is worse than omitting an old one of ours.
const labelManaged = "com.deepcove.managed"

// labelNode scopes a container to the deepcove instance that owns its session row.
//
// labelManaged alone says "deepcove made this", not "THIS deepcove made this", and the difference is
// dangerous: a sandbox with no matching session row is reported as leaked and offered up for destruction.
// Two instances sharing one Docker daemon would each declare the other's live sessions leaked. Observed
// exactly that when a second instance with its own DATA_DIR listed a running session as unowned.
//
// The value is derived from DATA_DIR because that is precisely the question being asked: whose database
// would hold the session row. It survives restarts and distinguishes instances on one machine.
const labelNode = "com.deepcove.node"

// NodeScope turns a data directory into the stable, opaque value stored in labelNode. Hashed rather than
// stored raw so a container label does not leak a filesystem path, and cleaned first so equivalent
// spellings of the same directory agree.
func NodeScope(dataDir string) string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(sum[:])[:16]
}

// ListSandboxes implements sandbox.Lister: every container this node created, running or not.
func (d *Driver) ListSandboxes(ctx context.Context) ([]sandbox.Instance, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		// Without All, a stopped container is invisible, which is exactly the case worth seeing: a
		// session whose container died is the thing you came to this view to find.
		All:     true,
		Filters: make(client.Filters).Add("label", d.ownershipLabels()...),
	})
	if err != nil {
		return nil, normalizeErr(err)
	}

	out := make([]sandbox.Instance, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, sandbox.Instance{
			Handle: sandbox.Handle(c.ID),
			State:  toInstanceState(c.State),
			Image:  c.Image,
			// Resources are left zero: the list endpoint does not carry them, and inspecting every
			// container to fill them in would turn one call into N for a field the session row
			// already records.
			CreatedAt: time.Unix(c.Created, 0).UTC(),
		})
	}
	slices.SortFunc(out, func(a, b sandbox.Instance) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(string(a.Handle), string(b.Handle))
	})
	return out, nil
}

// toInstanceState collapses Docker's seven states into the three the view cares about. The dividing line
// is whether the sandbox is still resident and holding memory, not whether it is executing, which is why
// paused counts as running: a paused container has not given its RAM back.
func toInstanceState(s container.ContainerState) sandbox.InstanceState {
	switch s {
	case container.StateRunning, container.StateRestarting, container.StatePaused:
		return sandbox.InstanceRunning
	case container.StateCreated, container.StateExited, container.StateDead, container.StateRemoving:
		return sandbox.InstanceStopped
	default:
		return sandbox.InstanceUnknown
	}
}

// ownershipLabels are the label filters that identify this instance's containers. Both are required:
// managed alone would claim another instance's sessions, and the node scope alone would be meaningless
// without knowing the container is a sandbox at all.
func (d *Driver) ownershipLabels() []string {
	labels := []string{labelManaged + "=true"}
	if d.nodeScope != "" {
		labels = append(labels, labelNode+"="+d.nodeScope)
	}
	return labels
}
