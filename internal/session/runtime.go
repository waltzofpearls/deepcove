package session

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Runtime names. These are persisted on the session row and sent over the wire, so treat them as a
// stable vocabulary rather than internal identifiers.
const (
	// RuntimeAppleVF runs each session in its own Linux VM via Apple Virtualization.framework. macOS
	// only, and only when the guest kernel and init are present.
	RuntimeAppleVF = "applevf"
	// RuntimeCloudHV runs each session in its own Linux VM via Cloud Hypervisor. Linux only, and only
	// when KVM is usable and the VM artifacts are present.
	RuntimeCloudHV = "cloudhv"
	// RuntimeSandboxless runs each session on the machine itself, with no VM and no container. For a
	// machine that is already the boundary. Needs tmux, which it cannot install.
	RuntimeSandboxless = "sandboxless"
)

// SandboxesOutliveDeepcove reports whether this runtime's sandboxes are still there once deepcove
// exits. A VM does not: Apple VF runs inside this process, and Cloud Hypervisor VMs a crashed deepcove
// left behind are stopped at its next start.
func SandboxesOutliveDeepcove(runtime string) bool {
	return runtime == RuntimeSandboxless
}

// vmRuntimes are the runtime names that boot a VM, one per hypervisor. A node offers at most one of
// them, because a host has one hypervisor worth using.
var vmRuntimes = []string{RuntimeAppleVF, RuntimeCloudHV}

// runtimeBootsVMs reports whether a runtime gives each machine its own kernel.
func runtimeBootsVMs(name string) bool {
	return slices.Contains(vmRuntimes, name)
}

// ErrUnknownRuntime means a session names a runtime this node does not offer, or, with Runtime empty, that
// the node has none at all. Both are real situations rather than bugs: a session created on a Mac with
// the VM runtime is unusable on a node without it, and the user needs to be told that rather than shown
// a confusing driver error.
type ErrUnknownRuntime struct {
	Runtime string
}

func (e ErrUnknownRuntime) Error() string {
	if e.Runtime == "" {
		return "session: this node has nothing to run sessions on yet"
	}
	return fmt.Sprintf("session: this node does not provide the %q runtime", e.Runtime)
}

// RegisterRuntime adds a runtime under name. NewService starts with none, so this is how every runtime a
// node supports joins, including ones installed after startup.
func (s *Service) RegisterRuntime(name string, sb sandbox.Sandbox) {
	if sb == nil {
		return
	}
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	s.runtimes[name] = sb
}

// lookupRuntime resolves one driver by name.
func (s *Service) lookupRuntime(name string) (sandbox.Sandbox, bool) {
	s.runtimesMu.RLock()
	defer s.runtimesMu.RUnlock()
	sb, ok := s.runtimes[name]
	return sb, ok
}

// registeredRuntimes copies the drivers, so a caller can walk them without holding the lock through
// whatever it does with each one.
func (s *Service) registeredRuntimes() map[string]sandbox.Sandbox {
	s.runtimesMu.RLock()
	defer s.runtimesMu.RUnlock()
	return maps.Clone(s.runtimes)
}

// SelectDefaultRuntime decides which runtime a create request gets when it names none.
//
// An empty preference takes the first runtime this node actually provides, in runtimeOrder, which puts
// the strongest isolation first, and prefers one that answers today. It never picks sandboxless: running
// with no isolation has to be asked for by name, by DEEPCOVE_DEFAULT_RUNTIME or on the create request. A
// VM that misses a health check still wins over it, since a create that fails with the reason beats one
// that quietly runs unisolated.
//
// A non-empty preference (DEEPCOVE_DEFAULT_RUNTIME) this node does not provide is returned as an error
// for the caller to log, but the node still gets the best default it has.
func (s *Service) SelectDefaultRuntime(ctx context.Context, preferred string) error {
	var err error
	if preferred != "" {
		if _, ok := s.lookupRuntime(preferred); ok {
			s.setDefaultRuntime(preferred)
			return nil
		}
		err = ErrUnknownRuntime{Runtime: preferred}
	}
	candidates := slices.DeleteFunc(s.Runtimes(), func(name string) bool { return name == RuntimeSandboxless })
	// Nothing to pick on its own, such as a Mac that has not downloaded its VM pieces. The node still
	// starts, so its UI can set a runtime up, and a create asks for one or says what is missing.
	if len(candidates) == 0 {
		s.setDefaultRuntime("")
		return err
	}

	// Prefer one that can actually host a session today. Registering a driver only says this node knows
	// how to talk to that backend, and defaulting to one that cannot answer would greet the user with a
	// failure they did not choose.
	unavailable := s.unavailableRuntimes(ctx)
	for _, name := range candidates {
		if _, dead := unavailable[name]; !dead {
			s.setDefaultRuntime(name)
			return err
		}
	}
	// Nothing is reachable. Still pick one so the node has a coherent default to report; creating a
	// session is refused separately, with the reason, rather than silently attempted.
	s.setDefaultRuntime(candidates[0])
	return err
}

// pickVMRuntime is the runtime a workspace VM runs on: the one asked for, else the node default when it
// boots VMs, else this node's VM runtime.
//
// No caller sends a runtime today, so reading the default alone refuses every request on a node whose
// default is empty or sandboxless, even when it has a VM runtime registered.
func (s *Service) pickVMRuntime(requested string) (string, error) {
	if requested != "" {
		if !runtimeBootsVMs(requested) {
			return "", fmt.Errorf("%w: a workspace VM can only run on a virtual machine", ErrInvalidInput)
		}
		return requested, nil
	}
	if defaultRuntime := s.DefaultRuntime(); runtimeBootsVMs(defaultRuntime) {
		return defaultRuntime, nil
	}
	for _, name := range s.Runtimes() {
		if runtimeBootsVMs(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: a workspace VM needs a VM runtime, and this node has none", ErrInvalidInput)
}

// runtimeOrder is the order runtimes are offered in, strongest isolation first. Map iteration is random,
// so without this the picker reshuffles between restarts and there is no stable "first" option to land
// on. A runtime not named here sorts last, alphabetically, rather than disappearing.
var runtimeOrder = []string{RuntimeAppleVF, RuntimeCloudHV, RuntimeSandboxless}

// Runtimes lists the runtimes this node provides, so the UI can offer them and the create path can
// validate a requested one. The order is deliberate and stable; see runtimeOrder.
func (s *Service) Runtimes() []string {
	registered := s.registeredRuntimes()
	out := make([]string, 0, len(registered))
	for name := range registered {
		out = append(out, name)
	}
	slices.SortFunc(out, func(a, b string) int {
		ra, rb := slices.Index(runtimeOrder, a), slices.Index(runtimeOrder, b)
		if ra < 0 {
			ra = len(runtimeOrder)
		}
		if rb < 0 {
			rb = len(runtimeOrder)
		}
		if ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	return out
}

// DefaultRuntime is what a create request gets when it does not ask for one.
func (s *Service) DefaultRuntime() string {
	s.runtimesMu.RLock()
	defer s.runtimesMu.RUnlock()
	return s.defaultRuntime
}

func (s *Service) setDefaultRuntime(name string) {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	s.defaultRuntime = name
}

// resolveRuntimeName turns an empty name, which is a request that did not choose one, into the default.
// It never returns an empty name without an error, so a caller can save what it returns.
func (s *Service) resolveRuntimeName(name string) (string, error) {
	if name != "" {
		return name, nil
	}
	name = s.DefaultRuntime()
	switch {
	case name != "":
		return name, nil
	// Runtimes but no default means only No sandbox is on offer, and that is never chosen for you.
	case len(s.Runtimes()) > 0:
		return "", fmt.Errorf("%w: this node has no VM runtime; %s", ErrInvalidInput, s.vmSetupAdvice())
	default:
		return "", fmt.Errorf("%w; %s", ErrUnknownRuntime{}, s.vmSetupAdvice())
	}
}

// vmSetupAdvice says what would give this node a VM runtime. "Set up" only when there is an installer.
func (s *Service) vmSetupAdvice() string {
	missing := s.MissingRuntimes()
	sawMissing := false
	for _, name := range vmRuntimes {
		code, ok := missing[name]
		if !ok {
			continue
		}
		sawMissing = true
		switch code {
		case MissingKVM:
			return "KVM is missing on this machine, or this user can't use it (if /dev/kvm exists, add this user to the kvm group, then reboot)"
		case MissingGuestInit:
			return "part of deepcove is missing, so reinstall deepcove"
		case MissingStartFailed:
			return "the VM runtime failed to start; " + logHint
		}
		if _, installable := s.runtimeInstaller(name); installable {
			return `pick "Set up deepcove" from the dashboard's profile menu to add one`
		}
	}
	if sawMissing {
		return "virtual machines can't run on this machine; " + logHint
	}
	return "this operating system has no virtual machine runtime yet"
}

// logHint says where deepcove's log is. Started by hand, it is the terminal; as a service, a file that
// "deepcove service status" names.
const logHint = `see deepcove's output in its terminal, or run "deepcove service status" if it runs as a service`

// runtimeByName resolves a runtime by name, falling back to the default when name is empty.
func (s *Service) runtimeByName(name string) (sandbox.Sandbox, error) {
	name, err := s.resolveRuntimeName(name)
	if err != nil {
		return nil, err
	}
	sb, ok := s.lookupRuntime(name)
	if !ok {
		return nil, ErrUnknownRuntime{Runtime: name}
	}
	return sb, nil
}

// runtimeFor resolves the driver hosting an existing session. Every operation on a live session goes
// through here, because a node can offer several runtimes and the session must go back to the one that
// created it.
func (s *Service) runtimeFor(sess *Session) (sandbox.Sandbox, error) {
	return s.runtimeByName(sess.Runtime)
}

// agentProvisionerFor is how a session asks its runtime about the agent: setup progress, what starts it,
// and where it works. Every runtime answers, so a driver that does not is a build this node cannot use.
func (s *Service) agentProvisionerFor(sess *Session) (sandbox.AgentProvisioner, error) {
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, err
	}
	provisioner, ok := runtime.(sandbox.AgentProvisioner)
	if !ok {
		return nil, fmt.Errorf("session: the %s runtime cannot answer for its agent", sess.Runtime)
	}
	return provisioner, nil
}

// ErrRuntimeUnavailable means the runtime exists on this node but cannot host a session right now, which
// is a different situation from a runtime this node has never heard of: the user does not need to pick
// something else, they need this one to answer again.
type ErrRuntimeUnavailable struct {
	Runtime string
	Reason  string
}

func (e ErrRuntimeUnavailable) Error() string { return e.Reason }

// runtimePingTimeout bounds asking a runtime whether it can host a session, for preflight and for every
// create, so a hung backend is reported instead of holding either up.
const runtimePingTimeout = 3 * time.Second

// runtimeUnavailable reports why one runtime cannot host a session, or "" when it can.
//
// Probes only the runtime asked about. Checking a single runtime by probing all of them would make every
// create wait on backends it is not going to use.
func (s *Service) runtimeUnavailable(ctx context.Context, name string) string {
	if name == "" {
		name = s.DefaultRuntime()
	}
	driver, ok := s.lookupRuntime(name)
	if !ok {
		return "" // an unknown runtime is reported by runtimeByName, not here
	}
	pinger, ok := driver.(sandbox.Pinger)
	if !ok {
		return "" // cannot report liveness, so assume usable rather than block on a signal we lack
	}
	pingCtx, cancel := context.WithTimeout(ctx, runtimePingTimeout)
	defer cancel()
	if _, err := pinger.Ping(pingCtx); err != nil {
		return unavailableReason(name)
	}
	return ""
}
