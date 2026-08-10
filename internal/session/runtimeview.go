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
	Handle string `json:"handle"`
	State  string `json:"state"`
	Image  string `json:"image,omitempty"`
	// Resources are what the driver actually gave the sandbox. All zero means the driver either applied
	// its own defaults or cannot report them, so the UI shows the session's request instead.
	Resources Resources `json:"resources"`
	CreatedAt time.Time `json:"created_at"`
	// SessionID and SessionName are empty when no session owns this sandbox. That is a leak: something
	// is still allocated for work that is over, and it will not go away on its own.
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
}

// Leaked reports a sandbox with no session behind it.
func (v SandboxView) Leaked() bool { return v.SessionID == "" }

// OrphanedSession is a session whose sandbox is gone. It cannot be started again as is, so the useful
// action is to delete it, not to hunt for the sandbox.
type OrphanedSession struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	// Handle is what the session row still points at, kept because it is what you would grep logs for.
	Handle string `json:"handle,omitempty"`
	Status Status `json:"status"`
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

	views := make([]RuntimeView, 0, len(names))
	for _, name := range names {
		views = append(views, s.runtimeView(ctx, name, sessions))
	}
	// After the registered ones, so the list reads as "what you have, then what you could have".
	for _, name := range s.missingRuntimeNames() {
		views = append(views, RuntimeView{
			Name:      name,
			Missing:   s.missingRuntimes[name],
			Sandboxes: []SandboxView{},
			Orphaned:  []OrphanedSession{},
		})
	}
	return views, nil
}

func (s *Service) runtimeView(ctx context.Context, name string, sessions []*Session) RuntimeView {
	view := RuntimeView{
		Name:      name,
		Default:   name == s.defaultRuntime,
		Reachable: true,
		Sandboxes: []SandboxView{},
		Orphaned:  []OrphanedSession{},
	}
	driver := s.runtimes[name]

	if pinger, ok := driver.(sandbox.Pinger); ok {
		pingCtx, cancel := context.WithTimeout(ctx, runtimeViewPingTimeout)
		defer cancel()
		version, err := pinger.Ping(pingCtx)
		if err != nil {
			view.Reachable = false
			view.Message = "This runtime's backend isn't reachable."
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

	// Index this runtime's sessions by the handle they claim. A session on another runtime is not this
	// runtime's business, and one that never got a sandbox has nothing to match.
	owners := make(map[string]*Session, len(sessions))
	for _, sess := range sessions {
		if sess.Runtime == name && sess.ContainerID != "" {
			owners[sess.ContainerID] = sess
		}
	}

	matched := make(map[string]bool, len(instances))
	for _, inst := range instances {
		handle := string(inst.Handle)
		sv := SandboxView{
			Handle:    handle,
			State:     string(inst.State),
			Image:     inst.Image,
			Resources: fromSandboxResources(inst.Resources),
			CreatedAt: inst.CreatedAt,
		}
		if sess, ok := owners[handle]; ok {
			matched[handle] = true
			sv.SessionID = sess.ID
			sv.SessionName = sess.Name
		}
		view.Sandboxes = append(view.Sandboxes, sv)
	}

	for _, sess := range sessions {
		if sess.Runtime != name || matched[sess.ContainerID] {
			continue
		}
		view.Orphaned = append(view.Orphaned, OrphanedSession{
			SessionID:   sess.ID,
			SessionName: sess.Name,
			Handle:      sess.ContainerID,
			Status:      sess.Status,
		})
	}
	return view
}

// ErrSandboxHasSession refuses to destroy a sandbox a session still owns. Deleting it underneath the
// session would leave a row pointing at nothing, trading one inconsistency for another; deleting the
// session is the operation that cleans up both.
var ErrSandboxHasSession = errors.New("session: this sandbox belongs to a session; delete the session instead")

// ErrSandboxNotFound means the runtime is not hosting that handle, usually because something else
// already cleaned it up.
var ErrSandboxNotFound = errors.New("session: this runtime is not hosting that sandbox")

// DestroyLeakedSandbox destroys a sandbox that no session owns. It re-checks ownership against a fresh
// listing rather than trusting the caller, because the view the user clicked from can be seconds stale
// and a session could have claimed the handle since.
func (s *Service) DestroyLeakedSandbox(ctx context.Context, runtimeName, handle string) error {
	driver, err := s.runtimeNamed(runtimeName)
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
	if !slices.ContainsFunc(instances, func(i sandbox.Instance) bool { return string(i.Handle) == handle }) {
		return ErrSandboxNotFound
	}

	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.Runtime == runtimeName && sess.ContainerID == handle {
			return ErrSandboxHasSession
		}
	}

	if err := driver.Destroy(ctx, sandbox.Handle(handle)); err != nil {
		return err
	}
	s.logger.Info("leaked sandbox destroyed", "runtime", runtimeName, "handle", handle)
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, RuntimesViewResponse{Runtimes: views})
}

// RuntimesViewResponse wraps the list in an object so the payload can grow a field later without
// breaking clients that already parse it.
type RuntimesViewResponse struct {
	Runtimes []RuntimeView `json:"runtimes"`
}

// handleDestroySandbox serves DELETE /api/runtimes/{runtime}/sandboxes/{handle}, the cleanup for a
// sandbox nothing owns. Refuses anything a session still points at.
func (s *Service) handleDestroySandbox(w http.ResponseWriter, r *http.Request) {
	err := s.DestroyLeakedSandbox(r.Context(), r.PathValue("runtime"), r.PathValue("handle"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrSandboxNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrSandboxHasSession):
		writeErrorCode(w, http.StatusConflict, codeInvalidInput, err.Error())
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

	// Both of these are resolved before committing to a streamed 200, so a caller gets a clean status
	// code rather than an error buried in the body.
	if _, missing := s.MissingRuntimes()[name]; !missing {
		writeErrorCode(w, http.StatusConflict, codeInvalidInput,
			"this runtime does not need setting up")
		return
	}
	install, ok := s.runtimeInstaller(name)
	if !ok {
		writeError(w, http.StatusNotImplemented, "this node cannot set up "+name+" for you")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported on this connection")
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
	err := install(r.Context(), func(p sandbox.PullProgress) {
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
