package docker

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestToInstanceState(t *testing.T) {
	cases := []struct {
		name string
		in   container.ContainerState
		want sandbox.InstanceState
	}{
		{"running is running", container.StateRunning, sandbox.InstanceRunning},
		{"restarting is a start in flight", container.StateRestarting, sandbox.InstanceStarting},
		{"paused has not given its memory back", container.StatePaused, sandbox.InstanceRunning},
		{"created exists but never ran", container.StateCreated, sandbox.InstanceStopped},
		{"exited is stopped", container.StateExited, sandbox.InstanceStopped},
		{"dead is stopped", container.StateDead, sandbox.InstanceStopped},
		{"removing is on its way out", container.StateRemoving, sandbox.InstanceStopped},
		{"a state we do not know is not guessed at", container.ContainerState("teleporting"), sandbox.InstanceUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toInstanceState(tc.in); got != tc.want {
				t.Errorf("toInstanceState(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A listing must only claim containers whose session rows live in THIS instance's database. Without the
// node label, two deepcoves sharing one Docker daemon each report the other's live sessions as leaked,
// and the Runtimes page offers to destroy them.
func TestOwnershipLabelsScopeToThisInstance(t *testing.T) {
	d := &Driver{nodeScope: "abc123"}
	got := d.ownershipLabels()

	want := []string{"com.deepcove.managed=true", "com.deepcove.node=abc123"}
	if len(got) != len(want) {
		t.Fatalf("filters = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filter[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNodeScope(t *testing.T) {
	// Equivalent spellings of one directory must agree, or a restart would orphan every container.
	a := NodeScope("/tmp/deepcove")
	b := NodeScope("/tmp/deepcove/")
	c := NodeScope("/tmp/./deepcove")
	if a != b || a != c {
		t.Errorf("equivalent paths produced different scopes: %q %q %q", a, b, c)
	}
	// Different data dirs must NOT collide: that is the whole point.
	if NodeScope("/tmp/one") == NodeScope("/tmp/two") {
		t.Error("different data dirs produced the same scope")
	}
	// Opaque: a container label should not carry a filesystem path around.
	if strings.Contains(a, "/") || strings.Contains(a, "tmp") {
		t.Errorf("scope %q leaks the path it came from", a)
	}
}

// An unscoped driver is the dangerous case, so it must not be constructible.
func TestNewRequiresANodeScope(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("New(\"\") returned no error; an unscoped driver would claim other instances' containers")
	}
}
