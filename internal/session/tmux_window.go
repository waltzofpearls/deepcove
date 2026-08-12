package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// AgentTmuxWindowName is the tmux window where the coding agent runs. It's created by the sandbox
// entrypoint Cmd (see service.go) and is **not deletable via the tmux-windows API**: killing the
// agent's window would end the tmux session and tear down the container.
const AgentTmuxWindowName = "main"

// Window-name validation:
//   - chars: lowercase letters, digits, hyphen
//   - length: 1 to maxTmuxWindowNameLength
//   - must not equal AgentTmuxWindowName ("main")
//
// tmux allows much wider names; we keep ours tight so URL-pathed
// names (.../tmux-windows/{name}/...) stay readable without escaping.
const maxTmuxWindowNameLength = 32

var tmuxWindowNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Sentinel errors. HTTP handlers map them to status codes.
var (
	ErrTmuxWindowExists       = errors.New("tmux window: already exists")
	ErrTmuxWindowNotFound     = errors.New("tmux window: not found")
	ErrTmuxWindowNameInvalid  = errors.New("tmux window: invalid name (must match [a-z0-9-]+, max 32 chars)")
	ErrTmuxWindowNameReserved = errors.New("tmux window: name 'main' is reserved")
	ErrSessionNotRunning      = errors.New("tmux window: session is not running")
	// ErrSessionStarting means the sandbox is up but its entrypoint has not finished installing the
	// agent and tmux yet, so anything that talks to tmux has nothing to talk to. Retrying later works.
	ErrSessionStarting = errors.New("session is still starting; try again in a moment")
)

// TmuxWindow is one tmux window in a session.
type TmuxWindow struct {
	Name  string `json:"name"`
	Index int    `json:"index"`
}

// TmuxWindowsResponse is the JSON body of GET /api/sessions/{id}/tmux-windows.
type TmuxWindowsResponse struct {
	Windows []TmuxWindow `json:"tmux_windows"`
	Active  string       `json:"active"` // name of the currently-focused window
}

// CreateTmuxWindowRequest is the body of POST /api/sessions/{id}/tmux-windows.
type CreateTmuxWindowRequest struct {
	Name string `json:"name,omitempty"` // optional; auto-generated when blank
}

// ListTmuxWindows returns the tmux windows of the session.
func (s *Service) ListTmuxWindows(ctx context.Context, id string) (*TmuxWindowsResponse, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.Status != StatusRunning {
		return nil, ErrSessionNotRunning
	}
	// Space-separated format. Tab would be slightly safer (window
	// names with spaces would parse correctly), BUT tmux 3.4 on
	// Alpine 3.20 doesn't honor literal tab bytes in -F format
	// strings; it silently replaces them with underscores. Space
	// is portable across tmux versions; our tmux-windows API rejects
	// names with spaces anyway (validated as [a-z0-9-]+), so the
	// only window we can't fully control is the agent's "main",
	// which we name ourselves in the sandbox entrypoint.
	out, err := s.execTmux(ctx, sess,
		"list-windows", "-t", tmuxSessionName(sess.ID),
		"-F", "#{window_index} #{window_name} #{window_active}")
	if err != nil {
		return nil, s.tmuxFailure(ctx, sess, fmt.Errorf("session: list tmux windows: %w", err))
	}
	return parseTmuxWindowsList(string(out)), nil
}

// CreateTmuxWindow creates a new tmux window in the session. If
// req.Name is empty, a name like "shell", "shell-2", "shell-3", ...
// is auto-assigned (first one not already in use).
func (s *Service) CreateTmuxWindow(ctx context.Context, id string, req CreateTmuxWindowRequest) (*TmuxWindow, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.Status != StatusRunning {
		return nil, ErrSessionNotRunning
	}

	// Validate caller-supplied name BEFORE any exec round-trip so
	// invalid / reserved names short-circuit cheaply.
	if req.Name != "" {
		if err := validateTmuxWindowName(req.Name); err != nil {
			return nil, err
		}
	}

	// Read existing windows for uniqueness check / auto-naming.
	// Race window between this and the new-window call below is
	// accepted: two concurrent auto-name creates could collide on
	// "shell", in which case the second errors out. v0 is single-user
	// and concurrency is low; revisit if it becomes a real problem.
	existing, err := s.ListTmuxWindows(ctx, id)
	if err != nil {
		return nil, err
	}
	existingNames := make(map[string]bool, len(existing.Windows))
	for _, w := range existing.Windows {
		existingNames[w.Name] = true
	}

	var name string
	if req.Name != "" {
		if existingNames[req.Name] {
			return nil, ErrTmuxWindowExists
		}
		name = req.Name
	} else {
		name = pickNextShellName(existingNames)
	}

	// -d: don't switch focus. -P -F: print the new window's index. The trailing colon targets the
	// SESSION rather than a window inside it; without it tmux tries to insert at an existing window's
	// index and fails.
	args := []string{
		"new-window", "-d", "-t", tmuxSessionName(sess.ID) + ":", "-n", name,
		"-P", "-F", "#{window_index}",
	}
	// SSH-mode shell window gets a wrapper that prints the MOTD before exec'ing the
	// shell. The MOTD file is dropped in the entrypoint preamble.
	if name == "shell" {
		args = append(args, "sh", "-c", "cat /etc/deepcove-motd 2>/dev/null; exec sh")
	}
	out, err := s.execTmux(ctx, sess, args...)
	if err != nil {
		return nil, fmt.Errorf("session: new-window: %w", err)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("session: parse new-window index %q: %w", strings.TrimSpace(string(out)), err)
	}
	return &TmuxWindow{Name: name, Index: idx}, nil
}

// SelectTmuxWindow makes the named window the session's active window.
// Subsequent `tmux attach` without an explicit -t window targets
// this window.
func (s *Service) SelectTmuxWindow(ctx context.Context, id, name string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status != StatusRunning {
		return ErrSessionNotRunning
	}
	if _, err := s.execTmux(ctx, sess,
		"select-window", "-t", tmuxSessionName(sess.ID)+":"+name); err != nil {
		if isTmuxWindowNotFound(err) {
			return ErrTmuxWindowNotFound
		}
		return fmt.Errorf("session: select-window: %w", err)
	}
	return nil
}

// DeleteTmuxWindow kills the named tmux window. Refuses to kill the
// agent's window (AgentTmuxWindowName) because that would end the
// tmux session and tear down the container.
func (s *Service) DeleteTmuxWindow(ctx context.Context, id, name string) error {
	if name == AgentTmuxWindowName {
		return ErrTmuxWindowNameReserved
	}
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status != StatusRunning {
		return ErrSessionNotRunning
	}
	if _, err := s.execTmux(ctx, sess,
		"kill-window", "-t", tmuxSessionName(sess.ID)+":"+name); err != nil {
		if isTmuxWindowNotFound(err) {
			return ErrTmuxWindowNotFound
		}
		return fmt.Errorf("session: kill-window: %w", err)
	}
	return nil
}

// execTmux runs `tmux <args...>` inside the session's container and
// returns stdout. Non-zero exit codes surface as errors with stderr
// content included for diagnostic context; callers can match on the
// error message via isTmuxWindowNotFound for the common "no such
// window" case.
//
// Stdout and stderr are drained concurrently. Non-TTY exec
// multiplexes both streams over the same hijacked socket; the
// sandbox driver demuxes them into two io.Pipes. If only stdout
// is being read, a stderr-heavy program (like tmux on errors)
// blocks the demuxer pump trying to write to the unbuffered
// stderr pipe; Wait then never returns and the whole call hangs.
// Concurrent drain is the standard fix.
func (s *Service) execTmux(ctx context.Context, sess *Session, args ...string) ([]byte, error) {
	execCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, err
	}
	cmd := append([]string{"tmux"}, args...)
	proc, err := runtime.Exec(execCtx, sandbox.ID(sess.SandboxID), sandbox.ExecSpec{
		Cmd: cmd,
		Tty: false,
	})
	if err != nil {
		return nil, fmt.Errorf("exec tmux %s: %w", args[0], err)
	}
	defer func() { _ = proc.Close() }()

	var (
		stdoutBuf, stderrBuf []byte
		stdoutErr, stderrErr error
		wg                   sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		stdoutBuf, stdoutErr = io.ReadAll(proc.Stdout())
	}()
	go func() {
		defer wg.Done()
		stderrBuf, stderrErr = io.ReadAll(proc.Stderr())
	}()

	exit, werr := proc.Wait(execCtx)
	wg.Wait()

	if werr != nil {
		return nil, fmt.Errorf("wait tmux: %w", werr)
	}
	if stdoutErr != nil {
		return nil, fmt.Errorf("read tmux stdout: %w", stdoutErr)
	}
	if stderrErr != nil {
		return nil, fmt.Errorf("read tmux stderr: %w", stderrErr)
	}
	if exit != 0 {
		return nil, fmt.Errorf("tmux %s exit %d: %s",
			args[0], exit, strings.TrimSpace(string(stderrBuf)))
	}
	return stdoutBuf, nil
}

// tmuxFailure explains a failed tmux call. Until the entrypoint has installed tmux and created the
// session there is nothing to talk to, and the raw exec failure ("exit -1") tells the user nothing.
// A VM makes this easy to hit: its setup takes far longer than a container's.
//
// Only on failure, and only on the paths a user drives (listing windows, and creating one through it),
// so the normal path and the attach path do not pay for the extra round trip.
func (s *Service) tmuxFailure(ctx context.Context, sess *Session, err error) error {
	// tmux answered and said the window does not exist. That is a real answer, and callers map it to a
	// typed error, so it must survive.
	if isTmuxWindowNotFound(err) {
		return err
	}
	if b, berr := s.Bootstrap(ctx, sess.ID); berr == nil && b.Status == BootstrapInstalling {
		return ErrSessionStarting
	}
	return err
}

// isTmuxWindowNotFound matches tmux's error text for a missing target
// window. Used to map exec failures into the typed ErrTmuxWindowNotFound.
func isTmuxWindowNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "can't find") || strings.Contains(msg, "no such")
}

// parseTmuxWindowsList parses tmux `list-windows -F` output where
// each line is "INDEX NAME ACTIVE" (ACTIVE is "1" or "0"). Fields
// are space-separated; we use strings.Fields so any whitespace count
// works. Window names are validated to [a-z0-9-]+ on the way in, so
// none contain whitespace, and the agent's "main" window doesn't
// either. (See the -F format note in ListTmuxWindows for why we
// don't use tab.)
func parseTmuxWindowsList(out string) *TmuxWindowsResponse {
	resp := &TmuxWindowsResponse{Windows: []TmuxWindow{}}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		resp.Windows = append(resp.Windows, TmuxWindow{Name: parts[1], Index: idx})
		if parts[2] == "1" {
			resp.Active = parts[1]
		}
	}
	return resp
}

// validateTmuxWindowNameSyntax checks only character class + length
// constraints. Used by *attach*, where "main" (the agent window) is
// a perfectly valid target; we just don't want to spawn a shell IN
// it (that's what the create-flow guard is for).
func validateTmuxWindowNameSyntax(name string) error {
	if name == "" || len(name) > maxTmuxWindowNameLength {
		return ErrTmuxWindowNameInvalid
	}
	if !tmuxWindowNamePattern.MatchString(name) {
		return ErrTmuxWindowNameInvalid
	}
	return nil
}

// validateTmuxWindowName is the stricter form used by create/select/
// delete; adds the reserved-name check on top of the syntax check.
// Refusing "main" here keeps the agent's window safe from accidental
// recreation or deletion via the API.
func validateTmuxWindowName(name string) error {
	if err := validateTmuxWindowNameSyntax(name); err != nil {
		return err
	}
	if name == AgentTmuxWindowName {
		return ErrTmuxWindowNameReserved
	}
	return nil
}

// pickNextShellName returns the first of "shell", "shell-2",
// "shell-3", ... that's not present in existing. Used when the
// caller creates a tmux window without supplying a name.
func pickNextShellName(existing map[string]bool) string {
	if !existing["shell"] {
		return "shell"
	}
	for i := 2; ; i++ {
		n := "shell-" + strconv.Itoa(i)
		if !existing[n] {
			return n
		}
	}
}
