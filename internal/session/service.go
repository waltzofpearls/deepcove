package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// toSandboxResources converts the session's human facing units to the byte counts the sandbox layer
// uses. Zero stays zero, which every driver reads as "no explicit limit".
func toSandboxResources(r Resources) sandbox.Resources {
	out := sandbox.Resources{CPU: r.CPU}
	if r.MemoryMB > 0 {
		out.Memory = r.MemoryMB << 20
	}
	if r.DiskMB > 0 {
		out.Disk = r.DiskMB << 20
	}
	return out
}

// workspaceDir is the conventional working directory. A user mount can target it (your
// code lives there), and it's the default the New Session form pre-fills. When no mount
// targets it, the entrypoint creates an empty /workspace so the agent always has a
// landing dir. The agent cd's to DEEPCOVE_WORKDIR (the first user mount, else this).
const workspaceDir = sandbox.SandboxedWorkDir

// underAgentHome reports whether a guest path is the agent's home or inside it. Nothing may be shared
// there: the home disk mounts over it, and the guest writes the agent's login into it.
func underAgentHome(guestPath string) bool {
	return guestPath == sandbox.SandboxedAgentHome || strings.HasPrefix(guestPath, sandbox.SandboxedAgentHome+"/")
}

// ErrMountInvalid is the sentinel for user mount validation failures.
// HTTP handlers map this to 400. The wrapped error message tells the
// caller which mount and why.
var ErrMountInvalid = errors.New("session: mount invalid")

// ErrInvalidInput is a user error rather than a failure. HTTP handlers map it to 400.
var ErrInvalidInput = errors.New("session: invalid input")

// placeholderOwner is the owner_email recorded for new sessions until
// the identity layer (tsnet WhoIs middleware, first-run owner
// persistence) lands. Search for this constant to find everywhere
// the eventual identity wiring needs to slot in.
const placeholderOwner = "local"

// ErrNotFound is returned when a session id doesn't exist. HTTP
// handlers map this to a 404. Surfaced by both the Service (which
// re-returns repo errors) and SessionRepository implementations
// (returned from Get / Mark* / UpdateActivity / Delete when zero
// rows match).
var ErrNotFound = errors.New("session: not found")

// Service is the session lifecycle layer. It composes a
// SessionRepository (for persistence) and the sandbox.Sandbox drivers
// that host sessions.
//
// All SQL lives in SessionRepository implementations; Service methods are pure orchestration.
// Owner_email is threaded through to the repo on every call; today every caller passes the
// placeholder owner, but the shape is ready for identity wiring.
type Service struct {
	repo   SessionRepository
	logger *slog.Logger

	// runtimes are the sandbox drivers this node provides, keyed by runtime name. A node can offer
	// more than one (a VM runtime and sandboxless), so each session records which one hosts it.
	//
	// Guarded: a runtime is installed from an HTTP request long after startup, and every request path
	// reads this while that happens.
	runtimesMu     sync.RWMutex
	runtimes       map[string]sandbox.Sandbox
	defaultRuntime string

	// missingRuntimes are runtimes this node knows about but cannot provide, keyed by name and valued
	// by a stable reason code, and runtimeInstallers is how to close that gap. Guarded because a runtime
	// can be installed from an HTTP request long after startup. runtimesSettingUp holds the ones being
	// installed right now. See missing.go.
	missingMu         sync.Mutex
	missingRuntimes   map[string]string
	runtimeInstallers map[string]RuntimeInstaller
	runtimesSettingUp map[string]bool

	// federation supplies the cross-node view for ListAggregated (set via SetFederation). Nil means
	// single-node: only this machine's sessions.
	federation Federation

	// llmProxyURL is where sessions send LLM requests instead of to the provider. Guarded: the UI can
	// change it while a session is being built or an agent launched. See llm_proxy.go.
	llmProxyMu  sync.RWMutex
	llmProxyURL string
	// llmProxyStore persists that URL. nil on a node whose proxy is set only by the flag.
	llmProxyStore LLMProxyStore
	// tailnetClient reaches a proxy that lives on the tailnet, for the reachability check. A function:
	// this node may sign in long after the service is built.
	tailnetClient    func() *http.Client
	sharedLogins     *sharedlogin.Keeper
	sharedLoginStore *SharedLoginStore
	// creatingLoginModes answers the guest's login check while a new session's row is not saved yet.
	creatingLoginModes sync.Map
	agentLocks         keyedLock
	// sharedLoginWaitLimit and sharedLoginWaitEvery are fields so tests can shorten the wait.
	sharedLoginWaitLimit time.Duration
	sharedLoginWaitEvery time.Duration

	// previewCache holds the most recent successful capture-pane
	// snapshot per session id. We serve it back when:
	//   - the session is stopped (so the UI can show a dimmed
	//     last-seen view + status overlay instead of a black void),
	//   - a live capture transiently fails (tmux not yet ready
	//     after a stop/start cycle).
	// In-memory only; rebuilt on service restart.
	previewCache *previewCache

	// Live terminal streams. Their WebSocket is hijacked, so http.Server's shutdown neither closes them
	// nor cancels their request context: this is the only handle a stop has on them.
	attached struct {
		mu      sync.Mutex
		closing bool
		streams map[*attachStream]struct{}
		wg      sync.WaitGroup
	}

	// background runs work that outlives the request that started it, like waiting for a machine to
	// finish setting up. A field so tests can run it synchronously instead of racing it.
	background func(func())
	// lifetime is the node's own run: restarts that outlive a request use it, so a shutdown still stops
	// them. restarts is waited on before the runtimes close, so none boots a VM after they are gone.
	lifetime context.Context
	restarts sync.WaitGroup

	// workspaceVM persists the shared machine. nil on a node that never wires one, where every session
	// gets a sandbox of its own.
	workspaceVM WorkspaceVMStore
	// workspaceMu serialises building the shared machine, so concurrent creates get one VM.
	workspaceMu sync.Mutex
	// shareRoots bounds which host folders may be shared into a sandbox. Empty Allowed means the service
	// does not check, and the driver still does.
	shareRoots sandbox.ShareRoots
	// dataDir is where this node keeps its state, such as files uploaded into a session.
	dataDir string

	// Sessions with a start in flight, for StatusStarting.
	starting struct {
		mu  sync.Mutex
		ids map[string]bool
	}
}

// NewService builds the service with NO runtimes. The caller registers each one it can provide and then
// picks the default, because none of them is privileged: Apple VF, Cloud Hypervisor, sandboxless, and
// whatever comes next are peers. Baking one in made every other runtime look like an add-on to it.
func NewService(repo SessionRepository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Service{
		repo:         repo,
		logger:       logger,
		runtimes:     map[string]sandbox.Sandbox{},
		previewCache: newPreviewCache(previewCacheMaxEntries),
		background:   func(f func()) { go f() },
		lifetime:     context.Background(),
		// The guest asks for the login every 10 seconds, and writing it takes moments.
		sharedLoginWaitLimit: 15 * time.Second,
		sharedLoginWaitEvery: 500 * time.Millisecond,
	}
}

// Image override is resolved + locked in at create time. SetupScript override is
// stored verbatim; empty means "use the agent's own setup at spec-build time" so registry
// updates flow into existing sessions on recreate.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Session, error) {
	if req.Agent == "" {
		return nil, fmt.Errorf("agent is required")
	}
	agent, err := resolveAgent(req.Agent)
	if err != nil {
		return nil, err
	}

	// Validate before any sandbox work so bad input fails as a clean 400 without
	// rollback. Owns session-layer concerns (reserved /root target, dup targets, mode);
	// the sandbox layer's validator owns host-path concerns (absolute, allowed root).
	if _, err := validateMountRequests(req.Mounts); err != nil {
		return nil, err
	}
	if err := req.Resources.Validate(); err != nil {
		return nil, err
	}
	if err := validateVMSize(req.Resources, SessionVMSizes); err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("session: generate id: %w", err)
	}
	now := time.Now().UTC()

	image := req.Image
	if image == "" {
		image = agent.Sandboxed.DefaultImage
	}

	sess := &Session{
		ID:             id.String(),
		Name:           req.Name,
		Agent:          req.Agent,
		Image:          image,
		OwnerEmail:     placeholderOwner,
		Status:         StatusCreated,
		Env:            req.Env,
		Mounts:         req.Mounts,
		SetupScript:    req.SetupScript,
		Resources:      req.Resources,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if sess.Name == "" {
		sess.Name = s.generateUniqueName(ctx, req.Agent)
	} else {
		name, err := validateSessionName(sess.Name)
		if err != nil {
			return nil, err
		}
		sess.Name = name
	}

	// Resolve the runtime before anything is created, so an unavailable one fails cleanly instead of
	// leaving a half built session behind.
	// A session on the shared machine runs on the machine's runtime, so that is the one to check for
	// availability below. Reading the record builds nothing; it just says which runtime that will be.
	sess.Runtime = req.Runtime
	if req.OnWorkspaceVM {
		vm, err := s.WorkspaceVM(ctx)
		if err != nil {
			return nil, err
		}
		sess.Runtime = vm.Runtime
	}
	// Resolved once and saved, so a setup that changes the default halfway through cannot leave the row
	// empty while the sandbox lands on the new default.
	if sess.Runtime, err = s.resolveRuntimeName(sess.Runtime); err != nil {
		return nil, err
	}
	runtime, err := s.runtimeByName(sess.Runtime)
	if err != nil {
		return nil, err
	}
	// Refuse before building anything. The driver would fail anyway, but deeper and in its own words; a
	// node with nothing usable should say so plainly rather than surface a backend's connection error.
	if reason := s.runtimeUnavailable(ctx, sess.Runtime); reason != "" {
		return nil, ErrRuntimeUnavailable{Runtime: sess.Runtime, Reason: reason}
	}

	if sess.LoginMode, err = s.loginModeOrDefault(req.LoginMode, agent, runtime, req.OnWorkspaceVM); err != nil {
		return nil, err
	}

	// A runtime with no guest boots nothing, so the row must not name an image the session never uses.
	if workDirIsOnThisMachine(runtime) {
		if unused := unusedOnThisMachine(req); unused != "" {
			return nil, fmt.Errorf("%w: a session with no sandbox runs the agent on the machine itself, so it takes no %s",
				ErrInvalidInput, unused)
		}
		sess.Image = ""
	} else if !req.OnWorkspaceVM && !sess.Resources.Asked() {
		// A session's own VM gets the default size unless you picked one.
		sess.Resources.Size = DefaultVMSize
	}

	// Made once the runtime is known, so a refused create leaves no folders behind.
	userMounts, err := s.prepareMountsCreatingFolders(req.Mounts)
	if err != nil {
		return nil, err
	}

	// Used if it is there, created if not, and only under the folders this node shares.
	if req.WorkDir != "" && !req.OnWorkspaceVM && workDirIsOnThisMachine(runtime) {
		workDir := filepath.Clean(req.WorkDir)
		if err := s.ensureWorkFolder(workDir); err != nil {
			return nil, fmt.Errorf("%w: work_dir: %s", ErrInvalidInput, err)
		}
		sess.WorkDir = workDir
	}

	if req.OnWorkspaceVM {
		if err := s.joinWorkspaceVM(ctx, sess, req); err != nil {
			return nil, err
		}
	} else {
		sandboxID, err := runtime.Create(ctx, s.buildSandboxSpec(sess, agent, userMounts, runtime))
		if err != nil {
			return nil, fmt.Errorf("session: create sandbox: %w", err)
		}
		sess.SandboxID = string(sandboxID)

		s.creatingLoginModes.Store(sess.ID, sess.LoginMode)
		defer s.creatingLoginModes.Delete(sess.ID)
		if err := runtime.Start(ctx, sandboxID); err != nil {
			_ = runtime.Destroy(context.Background(), sandboxID)
			return nil, fmt.Errorf("session: start sandbox: %w", err)
		}
	}
	sess.Status = StatusRunning

	if err := s.repo.Insert(ctx, sess); err != nil {
		// The shared machine is not this session's to tear down.
		if !sess.OnWorkspaceVM {
			sandboxID := sandbox.ID(sess.SandboxID)
			_ = runtime.Stop(context.Background(), sandboxID)
			_ = runtime.Destroy(context.Background(), sandboxID)
		}
		return nil, fmt.Errorf("session: insert row: %w", err)
	}
	s.background(func() { s.ensureAgentAfterSetup(sess, s.sessionWorkDir(sess)) })

	s.logger.Info("session created", "id", sess.ID, "name", sess.Name, "agent", sess.Agent)
	return sess, nil
}

// List returns sessions owned by the current owner (today, the
// placeholder owner). filter.IncludeAll=true returns stopped/errored
// sessions too; default is running only.
//
// Ordering is by last_activity_at DESC (defined by the repo): the canvas needs "what was I just
// doing?" as the top card, not "what did I most recently spawn?" The activity column is bumped on
// every attach connect / 30s tick / disconnect, so it tracks user engagement, not creation order.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]*Session, error) {
	sessions, err := s.repo.List(ctx, placeholderOwner, filter.IncludeAll)
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		s.reportStarting(sess)
		s.reportWorkDir(sess)
	}
	return sessions, nil
}

// Get returns the session with the given id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*Session, error) {
	sess, err := s.repo.Get(ctx, placeholderOwner, id)
	if err != nil {
		return nil, err
	}
	s.reportStarting(sess)
	s.reportWorkDir(sess)
	return sess, nil
}

// beginStarting marks a start as in flight so reads report it while the sandbox boots. ok is false
// when one is already running, which makes a second Start a no-op instead of two boots of one session.
func (s *Service) beginStarting(id string) (done func(), ok bool) {
	s.starting.mu.Lock()
	defer s.starting.mu.Unlock()
	if s.starting.ids[id] {
		return nil, false
	}
	if s.starting.ids == nil {
		s.starting.ids = make(map[string]bool)
	}
	s.starting.ids[id] = true
	return func() {
		s.starting.mu.Lock()
		delete(s.starting.ids, id)
		s.starting.mu.Unlock()
	}, true
}

// reportStarting overlays the in flight status. A running row is relabelled too, because a restart
// takes the machine down under a session that never stopped.
func (s *Service) reportStarting(sess *Session) {
	if sess == nil {
		return
	}
	s.starting.mu.Lock()
	inFlight := s.starting.ids[sess.ID]
	s.starting.mu.Unlock()
	if inFlight {
		sess.Status = StatusStarting
	}
}

// reportWorkDir answers where this session's agent works. The row knows only the folder you picked, so
// for a session on a folder deepcove made, this is the only place that path comes from.
func (s *Service) reportWorkDir(sess *Session) {
	if sess == nil {
		return
	}
	sess.AgentWorkDir = s.sessionWorkDir(sess)
}

// maxSessionNameLen bounds the name. Ids drive routing and storage, so this is a sanity cap rather
// than a hard constraint, but the name is no longer display-only: on the shared machine it becomes the
// session's folder.
const maxSessionNameLen = 100

// validateSessionName trims and checks a user-supplied name.
//
// A leading slash or a ".." segment would point the session's folder somewhere it was never meant to
// work in. Interior slashes are fine and nest, which is how you would write org/repo.
func validateSessionName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if len(trimmed) > maxSessionNameLen {
		return "", fmt.Errorf("%w: name too long (max %d characters)", ErrInvalidInput, maxSessionNameLen)
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%w: a name cannot start with /", ErrInvalidInput)
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: a name cannot contain a %q segment", ErrInvalidInput, segment)
		}
	}
	return trimmed, nil
}

// Rename sets a session's display name and returns the updated session.
// The name is trimmed; empty or over-long input is a user error.
func (s *Service) Rename(ctx context.Context, id, name string) (*Session, error) {
	name, err := validateSessionName(name)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Rename(ctx, placeholderOwner, id, name, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// RecordActivity bumps last_activity_at to NOW for a session. Called
// by the attach handler on connect, periodic 30s ticker while attached, and on disconnect. Cheap
// (single indexed UPDATE) and safe to call frequently. Errors are returned but never fatal; callers
// may log-and-ignore.
func (s *Service) RecordActivity(ctx context.Context, id string) error {
	return s.repo.UpdateActivity(ctx, placeholderOwner, id, time.Now().UTC())
}

// Start brings a stopped session back to running, and its setup runs again (not for a session on the
// workspace VM, which only rejoins the machine as it is). The agent's home and shared folders are kept.
// On a VM everything else in the guest starts fresh; with no sandbox, the machine's files are untouched.
// Running programs and scrollback start fresh either way.
//
// Idempotent on already-running sessions (no-op).
func (s *Service) Start(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status == StatusRunning {
		return nil
	}
	done, ok := s.beginStarting(id)
	if !ok {
		return nil // already starting, from a click that raced the restart on boot
	}
	defer done()
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return err
	}
	// Checked, not used: a session whose mounted folder went away should say so instead of starting.
	if _, err := s.prepareMountsRequiringFolders(sess.Mounts); err != nil {
		return err
	}
	if sess.OnWorkspaceVM {
		return s.startOnWorkspaceVM(ctx, sess)
	}
	if s.sandboxRunning(ctx, sess) {
		if err := s.repo.MarkRunning(ctx, placeholderOwner, id, time.Now().UTC()); err != nil {
			return fmt.Errorf("session: mark running: %w", err)
		}
		// Same wait as a fresh machine: running means the VM answers, not that setup has finished, and
		// tmux exists only once it has.
		s.background(func() { s.ensureAgentAfterSetup(sess, s.sessionWorkDir(sess)) })
		s.logger.Info("session started on a running machine", "id", id, "name", sess.Name)
		return nil
	}

	if err := runtime.Start(ctx, sandbox.ID(sess.SandboxID)); err != nil {
		// The sandbox this row points at is gone, most routinely a VM that died when deepcove restarted,
		// since that runtime's registry only ever lived in memory. Build a new one rather than leaving
		// the session permanently unstartable.
		if !sandboxVanished(err, sess.SandboxID) {
			return fmt.Errorf("session: sandbox start: %w", err)
		}
		if _, rerr := s.rebuildSandbox(ctx, sess, runtime); rerr != nil {
			return rerr
		}
	}
	if err := s.repo.MarkRunning(ctx, placeholderOwner, id, time.Now().UTC()); err != nil {
		return fmt.Errorf("session: mark running: %w", err)
	}
	s.background(func() { s.ensureAgentAfterSetup(sess, s.sessionWorkDir(sess)) })
	s.logger.Info("session started", "id", id, "name", sess.Name)
	return nil
}

// Stop transitions a running session to stopped: stops its sandbox
// and updates the row. Idempotent: stopping an
// already-stopped session is a no-op success.
func (s *Service) Stop(ctx context.Context, id string) error {
	// Held until the row says stopped, or a login change could start the agent again in between.
	unlock, err := s.lockAgent(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	// Starting is a no-op for the same reason stopped is: there is nothing running to stop yet. This is
	// also what happened before the status existed, when a booting session still read as stopped.
	if sess.Status == StatusStopped && sess.StopReason != "" {
		// Stopped by deepcove and waiting to come back, after a setup for one. Stopped by you now, so it
		// stays down.
		if err := s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC()); err != nil {
			return fmt.Errorf("session: mark stopped: %w", err)
		}
		return nil
	}
	if sess.Status == StatusStopped || sess.Status == StatusStarting {
		return nil
	}
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return err
	}
	if sess.OnWorkspaceVM || s.sandboxIsShared(ctx, sess) {
		// Others are using this machine, so end the session's agent and leave the machine up.
		_, _ = s.execTmux(ctx, sess, "kill-session", "-t", tmuxSessionName(sess.ID))
	} else if err := runtime.Stop(ctx, sandbox.ID(sess.SandboxID)); err != nil {
		// If the sandbox is already gone, finish the transition anyway.
		if !errors.Is(err, sandbox.ErrNotFound) {
			return fmt.Errorf("session: sandbox stop: %w", err)
		}
	}
	if err := s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC()); err != nil {
		return fmt.Errorf("session: mark stopped: %w", err)
	}
	return nil
}

// Destroy stops the sandbox (if running), removes it and the session's own storage, and removes the
// row. Any user-mounted folder is the user's own host dir and is never touched.
func (s *Service) Destroy(ctx context.Context, id string) error {
	// Held until the row is gone, or a login change could start an agent nothing will ever stop.
	unlock, err := s.lockAgent(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	handle := sandbox.ID(sess.SandboxID)
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return err
	}
	if sess.OnWorkspaceVM || s.sandboxIsShared(ctx, sess) {
		// Others are using this machine, so take the session off it rather than deleting it.
		_, _ = s.execTmux(ctx, sess, "kill-session", "-t", tmuxSessionName(sess.ID))
	} else if err := runtime.Destroy(ctx, handle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		// Best-effort teardown: if the runtime already lost the sandbox, we still want to clear our row.
		return fmt.Errorf("session: sandbox destroy: %w", err)
	}
	// A session on the shared machine has no storage of its own; the machine owns it.
	if !sess.OnWorkspaceVM {
		if err := runtime.RemoveStorage(ctx, sess.ID); err != nil &&
			!errors.Is(err, sandbox.ErrNotSupported) && !errors.Is(err, sandbox.ErrNotFound) {
			s.logger.Warn("could not remove session storage", "id", id, "err", err)
		}
	}
	// A VM's uploads go with its guest. Without a sandbox they are on this machine, so remove them here.
	if workDirIsOnThisMachine(runtime) {
		if err := os.RemoveAll(s.uploadsDirFor(runtime, sess.ID)); err != nil {
			s.logger.Warn("could not remove session uploads", "id", id, "err", err)
		}
	}
	if err := s.repo.Delete(ctx, placeholderOwner, id); err != nil {
		return fmt.Errorf("session: delete row: %w", err)
	}
	s.previewCache.Delete(id)
	return nil
}

// EditMounts recreates the sandbox with the new mounts. The session id and the agent's /root home
// survive. Everything else in the guest (installed packages, /etc edits, an unshared /workspace) does
// NOT survive. A shared folder lives on the host, so its contents are unaffected. Accepts 0..n mounts.
func (s *Service) EditMounts(ctx context.Context, id string, newMounts []MountRequest) (*Session, error) {
	// Held while the old machine goes and the new one comes, since that ends the agent too.
	unlock, err := s.lockAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// The shared machine's folder belongs to the machine, and this recreates the sandbox: doing that here
	// would tear the machine down under every other session on it.
	if sess.OnWorkspaceVM {
		return nil, fmt.Errorf("%w: this session is on the workspace VM, whose folder is edited on the machine itself", ErrInvalidInput)
	}
	if _, err := validateMountRequests(newMounts); err != nil {
		return nil, err
	}

	// The recreate stays on the session's existing runtime: changing mounts must not silently move a
	// session between runtimes.
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, err
	}
	// Refused here, before the old sandbox goes: this runtime takes no mounts, so the recreate below
	// would fail and leave the session with nothing to run on.
	if workDirIsOnThisMachine(runtime) {
		return nil, fmt.Errorf("%w: a session with no sandbox runs the agent on the machine itself, which shares no folders",
			ErrInvalidInput)
	}
	// Made once the refusals are past, so a rejected edit leaves no folders behind.
	userMounts, err := s.prepareMountsCreatingFolders(newMounts)
	if err != nil {
		return nil, err
	}

	oldHandle := sandbox.ID(sess.SandboxID)
	wasRunning := sess.Status == StatusRunning

	if wasRunning {
		if err := runtime.Stop(ctx, oldHandle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return nil, fmt.Errorf("session: stop sandbox: %w", err)
		}
	}
	if err := runtime.Destroy(ctx, oldHandle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return nil, fmt.Errorf("session: destroy old sandbox: %w", err)
	}

	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	spec := s.buildSandboxSpec(sess, agent, userMounts, runtime)

	newHandle, err := runtime.Create(ctx, spec)
	if err != nil {
		// Half-state: row exists but no sandbox. User's recovery is destroy.
		_ = s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC())
		return nil, fmt.Errorf("session: create new sandbox: %w", err)
	}
	if wasRunning {
		if err := runtime.Start(ctx, newHandle); err != nil {
			_ = runtime.Destroy(context.Background(), newHandle)
			_ = s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC())
			return nil, fmt.Errorf("session: start new sandbox: %w", err)
		}
	}

	now := time.Now().UTC()
	if err := s.repo.UpdateMountsAndSandbox(ctx, placeholderOwner, id, newMounts, string(newHandle), now); err != nil {
		return nil, fmt.Errorf("session: update mounts row: %w", err)
	}
	// Keep the just-reconfigured session at the top of the canvas.
	_ = s.repo.UpdateActivity(ctx, placeholderOwner, id, now)

	updated, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logger.Info("session mounts edited", "id", id, "name", sess.Name, "mounts", len(newMounts))
	return updated, nil
}

// sandboxRequest is what a sandbox needs to exist, whether it will host one session or several.
type sandboxRequest struct {
	// ID keys the runtime's per sandbox storage: a session's id for a private machine, the workspace
	// machine's own id for the shared one.
	ID    string
	Name  string
	Agent AgentSpec
	// Agents are every agent the machine runs. Empty means Agent alone, which is a session's own machine.
	Agents    []AgentSpec
	Image     string
	Resources Resources
	Setup     string
	Env       map[string]string
	Mounts    []sandbox.Mount
	WorkDir   string
	// OnThisMachine is a runtime with no guest, where the agent uses the user's own home.
	OnThisMachine bool
}

// llmProxyEndpoint is the address the agent calls. Inside a VM that is a localhost port the host
// forwards from. Without forwarding it is the proxy's own address.
func llmProxyEndpoint(runtime sandbox.Sandbox, proxyURL string) string {
	if proxyURL == "" {
		return ""
	}
	if f, ok := runtime.(sandbox.LLMForwarder); ok {
		return f.LLMEndpoint()
	}
	return proxyURL
}

// newSandboxSpec builds the driver spec: the caller's shared folders, the agent's env under the
// caller's, and the entrypoint that installs the agent and writes its launch script.
func newSandboxSpec(req sandboxRequest) sandbox.SandboxSpec {
	machineAgents := req.Agents
	if len(machineAgents) == 0 {
		machineAgents = []AgentSpec{req.Agent}
	}
	// Each agent's setup checks before it installs, so running all of them in turn is safe.
	var setups, names []string
	launchers := make(map[string]string, len(machineAgents))
	env := map[string]string{}
	for _, agent := range machineAgents {
		setups = append(setups, agent.Sandboxed.Setup)
		names = append(names, agent.Name)
		launchers[agent.Name] = agent.Sandboxed.LaunchCmd
		// The guest's env is the image's: its locale, its ripgrep. A session on the machine takes the
		// machine as it finds it.
		agentEnv := agent.Sandboxed.Env
		if req.OnThisMachine {
			agentEnv = agent.Sandboxless.Env
		}
		for k, v := range agentEnv {
			env[k] = v
		}
	}
	setup := req.Setup
	if setup == "" {
		setup = strings.Join(setups, "\n")
	}
	for k, v := range req.Env {
		env[k] = v
	}
	env["DEEPCOVE_WORKDIR"] = req.WorkDir

	return sandbox.SandboxSpec{
		// Stable across sandbox recreates, which is what lets a runtime keep per sandbox storage.
		SessionID: req.ID,
		Image:     req.Image,
		Agent: sandbox.Agent{
			Name:      req.Agent.Name,
			Binary:    req.Agent.Sandboxless.Binary,
			Install:   req.Agent.Sandboxless.Install,
			LaunchCmd: req.Agent.Sandboxless.LaunchCmd,
		},
		SharedLoginAgents: names,
		Resources:         toSandboxResources(req.Resources),
		Cmd:               buildEntrypoint(req.Name, setup, launchers),
		Env:               env,
		Mounts:            req.Mounts,
	}
}

// buildSandboxSpec describes a session's own machine. The runtime decides how LLM traffic leaves.
func (s *Service) buildSandboxSpec(sess *Session, agent AgentSpec, userMounts []sandbox.Mount, runtime sandbox.Sandbox) sandbox.SandboxSpec {
	return newSandboxSpec(sandboxRequest{
		ID:        sess.ID,
		Name:      sess.Name,
		Agent:     agent,
		Image:     sess.Image,
		Resources: sess.Resources.withSizeApplied(SessionVMSizes),
		Setup:     sess.SetupScript,
		Env:       sess.Env,
		Mounts:    userMounts,
		// The folder the agent is started in, which its launch script cds into first.
		WorkDir:       workDirOf(sess, userMounts, runtime),
		OnThisMachine: workDirIsOnThisMachine(runtime),
	})
}

// LaunchCmd is written to a file rather than passed inline to tmux; quoting a
// multi-line shell script as a tmux new-window argument is nightmare-tier. Heredocs
// use the single-quoted `<<'TERM'` form so user-supplied session names can't inject
// shell metacharacters into the MOTD.
func buildEntrypoint(sessName, setupScript string, launchCmds map[string]string) []string {
	// One launcher per agent the machine runs. Names come from the agent registry, so they are safe in a path.
	var launchers strings.Builder
	for _, agent := range slices.Sorted(maps.Keys(launchCmds)) {
		path := sandbox.SandboxedAgentLaunchPath(agent)
		fmt.Fprintf(&launchers, "cat > %s <<'DEEPCOVE_LAUNCH_END'\n#!/bin/sh\n%s\nDEEPCOVE_LAUNCH_END\nchmod +x %s\n",
			path, launchCmds[agent], path)
	}
	body := fmt.Sprintf(`mkdir -p /etc /usr/local/bin /var/log /workspace

# deepcove preamble: drop the shell tab's MOTD. Refreshed every boot so name/path
# changes propagate; not in user setup because it's not theirs to edit.
cat > /etc/deepcove-motd <<'DEEPCOVE_MOTD_END'

Welcome to deepcove session "%s".

  /workspace  ← your working folder (your mounted code, or empty scratch)
  /root       ← your home dir (persists across restarts)

The agent runs in the Agent tab once setup finishes.

DEEPCOVE_MOTD_END

# Default to installing so the UI sees the right state if it polls before setup writes.
echo installing > /etc/deepcove-bootstrap-status
: > /var/log/deepcove-bootstrap.log

# A subshell, so set -e ends setup rather than the whole script, which still has to record the failure.
(
  set -e
  echo "[deepcove] Starting setup at $(date -u +%%H:%%M:%%SZ)"
  # Persistent home, VM runtime only. The disk arrives raw because the base image ships no mkfs, so it
  # is formatted here where packages are already being installed. Only the first boot gets this far:
  # afterwards the guest init mounts it before this script runs. Every step is best effort, since a
  # session without a persistent home still works, it just starts fresh.
  # Trying the mount IS the test for whether the disk is formatted. blkid is no use here: it exits 0 on
  # a raw device, so asking it produced a confident "already formatted" and the format never ran.
  if [ -n "$DEEPCOVE_HOME_DEVICE" ] && ! grep -q " $DEEPCOVE_HOME_TARGET " /proc/mounts; then
    if ! mount "$DEEPCOVE_HOME_DEVICE" "$DEEPCOVE_HOME_TARGET" 2>/dev/null; then
      echo "[deepcove] Preparing persistent home on $DEEPCOVE_HOME_DEVICE"
      apk add --no-cache e2fsprogs >/dev/null 2>&1 || true
      mkfs.ext4 -q -F "$DEEPCOVE_HOME_DEVICE" || true
      # Copy the current home onto the new disk first. Mounting over it would hide the login the guest
      # already restored, and the agent would ask you to sign in again.
      mkdir -p /tmp/deepcove-home
      if mount "$DEEPCOVE_HOME_DEVICE" /tmp/deepcove-home 2>/dev/null; then
        cp -a "$DEEPCOVE_HOME_TARGET"/. /tmp/deepcove-home/ 2>/dev/null || true
        umount /tmp/deepcove-home 2>/dev/null || true
      fi
      mount "$DEEPCOVE_HOME_DEVICE" "$DEEPCOVE_HOME_TARGET" 2>/dev/null || true
    fi
  fi
%s
  echo "[deepcove] Setup complete"
) >> /var/log/deepcove-bootstrap.log 2>&1
setup_rc=$?

if [ $setup_rc -ne 0 ]; then
  echo errored > /etc/deepcove-bootstrap-status
  echo "[deepcove] Setup failed (exit $setup_rc); see log above" >> /var/log/deepcove-bootstrap.log
  # The machine stays up without this script; the UI shows this log from the failure screen.
  exit "$setup_rc"
fi

%s
# A fresh tmux refuses to reuse a stale socket from a previous boot.
rm -rf /tmp/tmux-* 2>/dev/null || true

echo ready > /etc/deepcove-bootstrap-status
`, sessName, setupScript, launchers.String())

	return []string{"sh", "-c", body}
}

// workDirIsOnThisMachine reports whether this runtime keeps session folders here rather than inside a
// guest, which is what makes work_dir a real path.
func workDirIsOnThisMachine(runtime sandbox.Sandbox) bool {
	provisioner, ok := runtime.(sandbox.AgentProvisioner)
	return ok && provisioner.WorkspaceRoot() != ""
}

// unusedOnThisMachine names the first thing a request asks for that a runtime with no guest has no use
// for. Stored and ignored is worse than refused: the session would run, quietly not doing what it was
// asked to do. The setup script is the sharpest of the three, since it is the one that installs things.
func unusedOnThisMachine(req CreateRequest) string {
	switch {
	case req.Image != "":
		return "image"
	case len(req.Mounts) > 0:
		return "shared folders"
	case req.SetupScript != "":
		return "setup script"
	case req.Resources.Asked():
		return "size"
	}
	return ""
}

// workDirOf asks the runtime where this session's agent works, with the mounts it is being created
// with rather than the ones on the row, which a recreate is about to change.
func workDirOf(sess *Session, userMounts []sandbox.Mount, runtime sandbox.Sandbox) string {
	if sess.WorkDir != "" {
		return sess.WorkDir
	}
	provisioner, ok := runtime.(sandbox.AgentProvisioner)
	if !ok {
		return sandbox.SandboxedWorkDir
	}
	return provisioner.WorkDir(sess.ID, userMounts)
}

var errFolderGone = errors.New("the folder is not there")

func (s *Service) prepareMountsCreatingFolders(reqs []MountRequest) ([]sandbox.Mount, error) {
	return s.prepareMounts(reqs, s.ensureSharedFolder)
}

func (s *Service) prepareMountsRequiringFolders(reqs []MountRequest) ([]sandbox.Mount, error) {
	return s.prepareMounts(reqs, s.requireSharedFolder)
}

func (s *Service) prepareMounts(reqs []MountRequest, folder func(string) error) ([]sandbox.Mount, error) {
	mounts, err := validateMountRequests(reqs)
	if err != nil {
		return nil, err
	}
	for i, m := range mounts {
		if err := folder(m.Source); err != nil {
			return nil, fmt.Errorf("%w: mounts[%d]: %s", ErrMountInvalid, i, err)
		}
	}
	return mounts, nil
}

func (s *Service) requireSharedFolder(path string) error {
	return requireFolder(path, s.shareRoots)
}

func (s *Service) ensureSharedFolder(path string) error {
	return ensureFolder(path, s.shareRoots)
}

// ensureWorkFolder checks a No sandbox work dir. Nothing goes into a guest, so only Allowed applies.
func (s *Service) ensureWorkFolder(path string) error {
	return ensureFolder(path, sandbox.ShareRoots{Allowed: s.shareRoots.Allowed})
}

// The allowed roots come first on purpose, so this cannot be used to find out what is on the machine.
func requireFolder(path string, roots sandbox.ShareRoots) error {
	if len(roots.Allowed) == 0 {
		return nil
	}
	if err := sandbox.MountSourceAllowed(path, roots); err != nil {
		return err
	}
	switch info, err := os.Stat(path); {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("%s is a file, not a folder", path)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s: %w", path, errFolderGone)
	default:
		return fmt.Errorf("cannot use %s: %s", path, err)
	}
}

func ensureFolder(path string, roots sandbox.ShareRoots) error {
	err := requireFolder(path, roots)
	if !errors.Is(err, errFolderGone) {
		return err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %s", path, err)
	}
	return nil
}

// Errors wrap ErrMountInvalid with a [N] index so the UI can point at the bad row.
// Host-path checks (absolute, cleaned, under an allowed root) live in the sandbox
// layer's validator.
func validateMountRequests(reqs []MountRequest) ([]sandbox.Mount, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	out := make([]sandbox.Mount, 0, len(reqs))
	seenTargets := make(map[string]int, len(reqs))
	for i, m := range reqs {
		if m.HostPath == "" {
			return nil, fmt.Errorf("%w: mounts[%d]: host_path is required", ErrMountInvalid, i)
		}
		if m.ContainerPath == "" {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path is required", ErrMountInvalid, i)
		}
		target := filepath.Clean(m.ContainerPath)
		// /root and anything under it may not be shared (see underAgentHome). /workspace is
		// intentionally NOT reserved: it's the conventional place to mount your code.
		if underAgentHome(target) {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q is reserved for the agent home",
				ErrMountInvalid, i, m.ContainerPath)
		}
		// Sub-paths of system dirs are deliberately allowed; only the root itself
		// is blocked (mounting over / would clobber the whole filesystem).
		if target == "/" {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q cannot be /",
				ErrMountInvalid, i, m.ContainerPath)
		}
		if prev, dup := seenTargets[target]; dup {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q is also used by mounts[%d]",
				ErrMountInvalid, i, m.ContainerPath, prev)
		}
		seenTargets[target] = i

		var readOnly bool
		switch strings.ToLower(m.Mode) {
		case "", "rw":
			readOnly = false
		case "ro":
			readOnly = true
		default:
			return nil, fmt.Errorf("%w: mounts[%d]: mode %q must be \"rw\" or \"ro\"",
				ErrMountInvalid, i, m.Mode)
		}

		out = append(out, sandbox.Mount{
			Source:   filepath.Clean(m.HostPath),
			Target:   target,
			ReadOnly: readOnly,
		})
	}
	return out, nil
}
