package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// The runtime view answers one question the rest of the API cannot: does what the driver is actually
// hosting match what the session table claims?
//
// The two drift in both directions. A destroy that half failed leaves a sandbox with no session behind
// it, still holding memory, and for a VM runtime nothing on the machine can show it: those VMs are
// threads inside this process, invisible to ps and Activity Monitor alike. In the other direction a
// restart empties the VM driver's in memory registry while every session row survives, so sessions
// legitimately outlive their sandboxes. Naming both cases is the point.

// RuntimeView is one runtime on this node: whether its backend answers, and what it is hosting.
type RuntimeView struct {
	Name string `json:"name"`
	// Default reports whether a create request with no runtime lands here.
	Default   bool   `json:"default"`
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`
	// Message is human facing guidance when Reachable is false.
	Message string `json:"message,omitempty"`
	// Missing is a stable reason code when this node knows about the runtime but cannot provide it, so
	// there is no driver to ask anything of. Reachable is false and every field below is empty.
	Missing string `json:"missing,omitempty"`
	// ListSupported is false for a driver that cannot enumerate its sandboxes, which is a capability
	// gap rather than an error. Sandboxes and Orphaned are then empty and mean nothing.
	ListSupported bool `json:"list_supported"`
	// ListError explains a listing that was supported but failed, so an empty list is never silently
	// read as "nothing is running".
	ListError string        `json:"list_error,omitempty"`
	Sandboxes []SandboxView `json:"sandboxes"`
	// Orphaned are sessions on this runtime whose sandbox the driver no longer has. Reported per
	// runtime rather than mixed into Sandboxes, because there is no sandbox left to describe.
	Orphaned []OrphanedSession `json:"orphaned"`
}

// SandboxView is one sandbox the driver is hosting, joined to the session that owns it.
type SandboxView struct {
	SandboxID string `json:"sandbox_id"`
	State     string `json:"state"`
	// Unresponsive means the machine is up but not answering. A restart is what fixes it.
	Unresponsive bool   `json:"unresponsive,omitempty"`
	Image        string `json:"image,omitempty"`
	// Resources are what the driver actually gave the sandbox. All zero means the driver either applied
	// its own defaults or cannot report them, so the UI shows the session's request instead.
	Resources Resources `json:"resources"`
	CreatedAt time.Time `json:"created_at"`
	// SharedFolders are the host folders this sandbox shares, as the driver reports them.
	SharedFolders []SandboxSharedFolder `json:"shared_folders"`
	// Sessions are the sessions living on this sandbox, in creation order. A shared machine has several.
	// Empty is a leak: something is still allocated for work that is over, and it will not go away on
	// its own.
	Sessions []SandboxSession `json:"sessions"`
	// IsWorkspaceVM marks this node's shared machine, which is allowed to sit there with no sessions on
	// it and is deleted through its own endpoint rather than as a leak.
	IsWorkspaceVM bool `json:"is_workspace_vm,omitempty"`
}

// SandboxSharedFolder is one host folder shared into a sandbox, and where it lands inside it.
type SandboxSharedFolder struct {
	HostPath  string `json:"host_path"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

// SandboxSession is one session on a sandbox, enough to name it and link to it.
type SandboxSession struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status Status `json:"status"`
}

// Leaked reports a sandbox nothing owns: no sessions, and not the shared machine.
func (v SandboxView) Leaked() bool { return len(v.Sessions) == 0 && !v.IsWorkspaceVM }

// OrphanedSession is a session whose sandbox is gone. It cannot be started again as is, so the useful
// action is to delete it, not to hunt for the sandbox.
type OrphanedSession struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	// SandboxID is what the session row still points at, kept because it is what you would grep logs
	// for.
	SandboxID string `json:"sandbox_id,omitempty"`
	Status    Status `json:"status"`
}

const runtimeViewPingTimeout = 3 * time.Second

// RuntimeViews reports every runtime this node provides, each joined against the session table.
func (s *Service) RuntimeViews(ctx context.Context) ([]RuntimeView, error) {
	// Every session, not just the running ones: a stopped session still owns its sandbox, and hiding
	// those would report their sandboxes as leaked.
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return nil, err
	}

	// Runtimes() is already ordered deliberately, so the view and the create picker agree on what comes
	// first rather than each imposing its own idea.
	names := s.Runtimes()

	// The shared machine, if this node has one. Failing to read it only costs the marking, so the view
	// still renders.
	workspaceVM, err := s.WorkspaceVM(ctx)
	if err != nil {
		workspaceVM = nil
	}

	views := make([]RuntimeView, 0, len(names))
	for _, name := range names {
		views = append(views, s.runtimeView(ctx, name, sessions, workspaceVM))
	}
	// After the registered ones, so the list reads as "what you have, then what you could have".
	missing := s.MissingRuntimes()
	for _, name := range s.missingRuntimeNames() {
		views = append(views, RuntimeView{
			Name:      name,
			Missing:   missing[name],
			Sandboxes: []SandboxView{},
			Orphaned:  []OrphanedSession{},
		})
	}
	return views, nil
}

func (s *Service) runtimeView(
	ctx context.Context,
	name string,
	sessions []*Session,
	workspaceVM *WorkspaceVM,
) RuntimeView {
	view := RuntimeView{
		Name:      name,
		Default:   name == s.DefaultRuntime(),
		Reachable: true,
		Sandboxes: []SandboxView{},
		Orphaned:  []OrphanedSession{},
	}
	driver, _ := s.lookupRuntime(name)

	if pinger, ok := driver.(sandbox.Pinger); ok {
		pingCtx, cancel := context.WithTimeout(ctx, runtimeViewPingTimeout)
		defer cancel()
		version, err := pinger.Ping(pingCtx)
		if err != nil {
			view.Reachable = false
			view.Message = unavailableReason(name)
			return view
		}
		view.Version = version
	}

	lister, ok := driver.(sandbox.Lister)
	if !ok {
		return view
	}
	view.ListSupported = true

	instances, err := lister.ListSandboxes(ctx)
	if err != nil {
		view.ListError = err.Error()
		return view
	}

	// Index this runtime's sessions by the sandbox they claim, several per sandbox since a machine can
	// be shared. A session on another runtime is not this runtime's business.
	onSandbox := make(map[string][]SandboxSession, len(sessions))
	for _, sess := range sessions {
		if sess.Runtime == name && sess.SandboxID != "" {
			onSandbox[sess.SandboxID] = append(onSandbox[sess.SandboxID],
				SandboxSession{ID: sess.ID, Name: sess.Name, Status: sess.Status})
		}
	}

	matched := make(map[string]bool, len(instances))
	for _, inst := range instances {
		sandboxID := string(inst.ID)
		sv := SandboxView{
			SandboxID:     sandboxID,
			State:         string(inst.State),
			Unresponsive:  inst.Unresponsive,
			Image:         inst.Image,
			Resources:     fromSandboxResources(inst.Resources),
			CreatedAt:     inst.CreatedAt,
			SharedFolders: sandboxSharedFolders(inst.SharedFolders),
			Sessions:      []SandboxSession{},
			IsWorkspaceVM: workspaceVM != nil && sandboxID == workspaceVM.SandboxID,
		}
		if found, ok := onSandbox[sandboxID]; ok {
			matched[sandboxID] = true
			sv.Sessions = found
		}
		view.Sandboxes = append(view.Sandboxes, sv)
	}

	// Only a session that says it is RUNNING can be orphaned. A stopped one has no machine by
	// definition, and a starting one is having its machine built right now.
	for _, sess := range sessions {
		if sess.Runtime != name || sess.Status != StatusRunning {
			continue
		}
		if sess.SandboxID == "" || matched[sess.SandboxID] {
			continue
		}
		view.Orphaned = append(view.Orphaned, OrphanedSession{
			SessionID:   sess.ID,
			SessionName: sess.Name,
			SandboxID:   sess.SandboxID,
			Status:      sess.Status,
		})
	}
	return view
}

// ErrSandboxHasSession refuses to destroy a sandbox a session still owns. Deleting it underneath the
// session would leave a row pointing at nothing, trading one inconsistency for another; deleting the
// session is the operation that cleans up both.
var ErrSandboxHasSession = errors.New("session: this sandbox belongs to a session; delete the session instead")

// sandboxSharedFolders converts the driver's shared folders into the view's shape, host path first because
// that is the side you recognise.
func sandboxSharedFolders(sharedFolders []sandbox.SharedFolder) []SandboxSharedFolder {
	out := make([]SandboxSharedFolder, 0, len(sharedFolders))
	for _, m := range sharedFolders {
		out = append(out, SandboxSharedFolder{HostPath: m.HostPath, GuestPath: m.GuestPath, ReadOnly: m.ReadOnly})
	}
	return out
}

// ErrSandboxIsWorkspaceVM refuses to delete the shared machine as if it were a leak.
var ErrSandboxIsWorkspaceVM = errors.New("session: this is the workspace VM; delete it from its own page")

// ErrSandboxNotFound means the runtime is not hosting that sandbox, usually because something else
// already cleaned it up.
var ErrSandboxNotFound = errors.New("session: this runtime is not hosting that sandbox")

// DestroyLeakedSandbox destroys a sandbox that no session owns. It re-checks ownership against a fresh
// listing rather than trusting the caller, because the view the user clicked from can be seconds stale
// and a session could have claimed the sandbox since.
func (s *Service) DestroyLeakedSandbox(ctx context.Context, runtimeName, sandboxID string) error {
	// Not a leak, and its own endpoint also clears the record instead of leaving one pointing at nothing.
	if vm, err := s.WorkspaceVM(ctx); err == nil && vm.SandboxID == sandboxID {
		return ErrSandboxIsWorkspaceVM
	}
	driver, err := s.runtimeByName(runtimeName)
	if err != nil {
		return err
	}
	lister, ok := driver.(sandbox.Lister)
	if !ok {
		return fmt.Errorf("session: the %q runtime cannot enumerate its sandboxes: %w", runtimeName, sandbox.ErrNotSupported)
	}
	instances, err := lister.ListSandboxes(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(instances, func(i sandbox.Instance) bool { return string(i.ID) == sandboxID }) {
		return ErrSandboxNotFound
	}

	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.Runtime == runtimeName && sess.SandboxID == sandboxID {
			return ErrSandboxHasSession
		}
	}

	if err := driver.Destroy(ctx, sandbox.ID(sandboxID)); err != nil {
		return err
	}
	s.logger.Info("leaked sandbox destroyed", "runtime", runtimeName, "sandbox", sandboxID)
	return nil
}

// fromSandboxResources converts back to the human facing units the API speaks. The inverse of
// toSandboxResources, and lossy in the same harmless way: sub-MiB values round down to zero, and the
// drivers do not report any.
func fromSandboxResources(r sandbox.Resources) Resources {
	return Resources{
		CPU:      r.CPU,
		MemoryMB: r.Memory >> 20,
		DiskMB:   r.Disk >> 20,
	}
}

// handleRuntimes serves GET /api/runtimes: this node's runtimes and what each is hosting.
//
// Deliberately this node only. Runtime state is a property of the machine the sandbox runs on, and the
// question being asked ("did my delete actually remove the VM") is asked about one machine at a time.
func (s *Service) handleRuntimes(w http.ResponseWriter, r *http.Request) {
	views, err := s.RuntimeViews(r.Context())
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, RuntimesViewResponse{Runtimes: views})
}

// RuntimesViewResponse wraps the list in an object so the payload can grow a field later without
// breaking clients that already parse it.
type RuntimesViewResponse struct {
	Runtimes []RuntimeView `json:"runtimes"`
}

// handleDestroySandbox serves DELETE /api/runtimes/{runtime}/sandboxes/{sandbox_id}, the cleanup for a
// sandbox nothing owns. Refuses anything a session still points at.
func (s *Service) handleDestroySandbox(w http.ResponseWriter, r *http.Request) {
	err := s.DestroyLeakedSandbox(r.Context(), r.PathValue("runtime"), r.PathValue("sandbox_id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrSandboxNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrSandboxHasSession), errors.Is(err, ErrSandboxIsWorkspaceVM):
		writeErrorCode(w, http.StatusConflict, codeConflict, err.Error())
	default:
		var unknown ErrUnknownRuntime
		if errors.As(err, &unknown) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeSandboxError(w, err)
	}
}

// handleSetupRuntime serves POST /api/runtimes/{runtime}/setup: it downloads whatever a missing runtime
// needs, then registers it, so a node goes from "cannot run VMs" to running them without a restart.
//
// Progress streams as newline delimited JSON in the same shape the image pull already emits, so the
// client has one reader for both.
func (s *Service) handleSetupRuntime(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("runtime")

	// All of these are resolved before committing to a streamed 200, so a caller gets a clean status
	// code rather than an error buried in the body.
	finish, err := s.startRuntimeSetup(name)
	if errors.Is(err, errRuntimeNotMissing) {
		if _, ok := s.lookupRuntime(name); ok {
			// Set up already, from another tab or client. Done rather than a conflict, so the caller
			// refreshes instead of offering a setup that can never run.
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"percent": 100, "done": true})
			return
		}
	}
	if err != nil {
		writeErrorCode(w, http.StatusConflict, codeConflict, err.Error())
		return
	}
	defer finish()
	install, ok := s.runtimeInstaller(name)
	if !ok {
		writeError(w, http.StatusNotImplemented, "this node cannot set up "+name+" for you")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, "streaming unsupported on this connection")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	emit := func(v any) {
		_ = enc.Encode(v)
		flusher.Flush()
	}

	// Throttled to whole percent or status changes, so a download does not emit thousands of lines.
	lastPct, lastStatus := -1, ""
	err = install(r.Context(), func(p sandbox.PullProgress) {
		pct := -1
		if p.Total > 0 {
			pct = int(p.Current * 100 / p.Total)
		}
		if pct == lastPct && p.Status == lastStatus {
			return
		}
		lastPct, lastStatus = pct, p.Status
		emit(map[string]any{"status": p.Status, "percent": pct})
	})
	if err != nil {
		s.logger.Warn("could not set up runtime", "runtime", name, "err", err)
		emit(map[string]any{"error": err.Error()})
		return
	}
	s.logger.Info("runtime set up and registered", "runtime", name)
	emit(map[string]any{"percent": 100, "done": true})
}
