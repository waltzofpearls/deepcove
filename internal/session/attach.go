package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// activityTickInterval is how often we bump last_activity_at while a
// session is attached. Cheap UPDATE; chosen as a balance between
// "fresh enough that the canvas reorder feels right" and "not a
// write per keystroke."
const activityTickInterval = 30 * time.Second

// AttachSubprotocol is the WebSocket subprotocol the server speaks.
// Both ends declare it on the handshake so the wire contract is
// explicit; future versions can bump the v1 suffix.
const AttachSubprotocol = "deepcove.pty.v1"

// AttachControlMessage is the JSON shape of WS text frames the client may send: "resize", "detach",
// "notice" and "visibility". New types can be added without breaking older clients (unknown types are
// ignored).
type AttachControlMessage struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Text string `json:"text,omitempty"`
	// Visible is whether the browser tab showing this terminal is on screen.
	Visible bool `json:"visible,omitempty"`
}

// noticeMillis is how long a notice stays in the status line, long enough to read a file name and a reason.
const noticeMillis = "5000"

// handleAttach upgrades to a WebSocket and bridges it to a
// `tmux attach -t main` exec inside the session's sandbox. Binary
// frames are bytes flowing both ways (terminal I/O); text frames
// are JSON control messages (AttachControlMessage).
//
// Optional ?hidden=true says the browser tab opened in the background, so the attach does not count as
// looking at the session until a "visibility" message says it is on screen.
//
// Optional ?tmux_window= query param attaches the client to a specific tmux window. tmux tracks
// "current window" per-client, so two simultaneous attaches with different ?tmux_window= values each
// see their own window: the substrate the upcoming Split mode (one session, two side-by-side
// terminals on different windows) is built on. Absent or empty means legacy behavior (follow the
// session's globally-current window).
func (s *Service) handleAttach(w http.ResponseWriter, r *http.Request) {
	// Registered before anything else so its release runs last, after the cleanup below. A stop cancels
	// this context and waits here, which is what keeps the sandbox and the database around for it.
	ctx, release, ok := s.beginAttach(r.Context())
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "node is shutting down")
		return
	}
	defer release()

	id := r.PathValue("id")
	sess, err := s.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		writeNotHostedHere(w)
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	if sess.Status != StatusRunning {
		writeError(w, http.StatusConflict, "session is not running")
		return
	}

	// Parse + validate optional window targeting BEFORE upgrading the WS so an invalid name fails as
	// a clean 400 instead of an awkward post-upgrade close. Uses the SYNTAX-only validator; the
	// stricter validateTmuxWindowName forbids "main" because no caller should *create* a window with
	// that name (it'd collide with the agent's window), but here we WANT to allow attaching to "main"
	// since that's the agent's window itself.
	tmuxWindow := r.URL.Query().Get("tmux_window")
	if tmuxWindow != "" {
		if err := validateTmuxWindowNameSyntax(tmuxWindow); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// A page can make a browser send a plain GET with no Origin, an image for one. Only a WebSocket
	// handshake goes on to start anything in the sandbox.
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, http.StatusUpgradeRequired, "attach needs a WebSocket")
		return
	}

	// Each attach runs in its own per-connection tmux session, created as a member of the "main"
	// session group. Why: tmux's `select-window` operates on the SESSION's active-window state, not
	// per-client, so two simultaneous attaches with different ?tmux_window= values would clobber each
	// other, and Split mode (two attaches in parallel) would show the same window on both sides.
	//
	// Session-group members share the linked window list (so the windows API still operates on "main"
	// canonically), but each member has its own current-window: true per-client window views. Killing
	// the view session on disconnect (deferred below) also tears down the attached tmux client
	// process, so tmux clients don't pile up inside the sandbox after each toggle.
	viewSession := "view-" + uuid.NewString()[:8]
	setupCtx, cancelSetup := context.WithTimeout(ctx, 3*time.Second)

	// The agent's session can be missing: its background start failed, or the machine rebooted.
	// Idempotent, so just create it rather than fail the attach.
	if err := s.startAgentSession(setupCtx, sess, s.sessionWorkDir(sess)); err != nil {
		s.logger.Warn("attach: could not ensure the agent session", "id", id, "err", err)
	}
	// Only `deepcove connect` asks for it, so its tabs match the web's. The web opens the shell tab
	// itself when you pick Shell.
	if r.URL.Query().Get("shell_tab") == "true" {
		_, err := s.CreateTmuxWindow(setupCtx, id, CreateTmuxWindowRequest{Name: "shell"})
		if err != nil && !errors.Is(err, ErrTmuxWindowExists) {
			s.logger.Warn("attach: could not open the shell tab", "id", id, "err", err)
		}
	}

	if _, err := s.execTmux(setupCtx, sess,
		"new-session", "-d", "-s", viewSession, "-t", tmuxSessionName(sess.ID),
	); err != nil {
		cancelSetup()
		s.logger.Warn("attach: view session setup failed", "id", id, "err", err)
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, "view session setup: "+err.Error())
		return
	}
	// Only `deepcove connect` asks for it, so the web terminal's bar stays as it was.
	if r.URL.Query().Get("detach_hint") == "true" {
		if _, err := s.execTmux(setupCtx, sess, "set-option", "-t", viewSession, "status-right", `Ctrl+\ to disconnect `); err != nil {
			s.logger.Warn("attach: detach hint failed", "id", id, "err", err)
		}
	}
	cancelSetup()
	defer func() {
		// Cleanup on every return path. Killing the view session
		// detaches the in-sandbox tmux client and the exec
		// process exits, preventing the accumulation.
		killCtx, cancelKill := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelKill()
		if _, err := s.execTmux(killCtx, sess, "kill-session", "-t", viewSession); err != nil {
			s.logger.Warn("attach: view session cleanup failed",
				"id", id, "view", viewSession, "err", err)
		}
	}()

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{AttachSubprotocol},
		// The library's default origin check: a browser may open this socket only from a page this node
		// served. The in-browser Tailscale node sends no Origin header, which the library allows.
	})
	if err != nil {
		s.logger.Warn("attach: ws accept failed", "id", id, "err", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()

	// `-u` forces UTF-8 client mode. Without it, tmux substitutes non-ASCII bytes with
	// `_` when sending to clients (Alpine has no locale set, so tmux defaults to 8-bit).
	cmd := []string{"tmux", "-u", "attach", "-t", viewSession}
	if tmuxWindow != "" {
		cmd = append(cmd, ";", "select-window", "-t", viewSession+":"+tmuxWindow)
	}
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		s.logger.Warn("attach: unknown runtime", "id", id, "err", err)
		return
	}
	proc, err := runtime.Exec(ctx, sandbox.ID(sess.SandboxID), sandbox.ExecSpec{
		Cmd: cmd,
		Tty: true,
		// TERM is required for tmux to function; without it tmux
		// can run but doesn't recognise the terminal type and
		// some inputs/escape sequences misbehave.
		Env: map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "exec failed: "+err.Error())
		return
	}
	defer func() { _ = proc.Close() }()

	// Record activity on attach. Idle ticker keeps last_activity_at
	// fresh for the canvas sort; one final write on disconnect
	// captures the exact moment the user walked away. All three
	// writes are best-effort; a DB hiccup shouldn't kill the
	// terminal session.
	logger := s.logger.With("session_id", id)
	view := &sessionView{service: s, id: id}
	defer view.end()
	// A tab that opens in the background has not looked at the session yet. Anything else has.
	if r.URL.Query().Get("hidden") != "true" {
		if rerr := view.setVisible(ctx, true); rerr != nil {
			logger.Warn("attach: record activity on open", "err", rerr)
		}
	}
	tickerCtx, cancelTicker := context.WithCancel(ctx)
	defer cancelTicker()
	go func() {
		t := time.NewTicker(activityTickInterval)
		defer t.Stop()
		for {
			select {
			case <-tickerCtx.Done():
				return
			case <-t.C:
				// A terminal in a background tab is not activity, and would mark a finished turn as seen.
				if !view.isVisible() {
					continue
				}
				if rerr := s.RecordActivity(tickerCtx, id); rerr != nil {
					logger.Warn("attach: record activity tick", "err", rerr)
				}
			}
		}
	}()

	// -E skips tmux's own "[detached (from session view-…)]" line, since connect prints its own.
	detach := func(ctx context.Context) error {
		_, err := s.execTmux(ctx, sess, "detach-client", "-E", "true", "-s", viewSession)
		return err
	}
	// Only this attach's client gets the notice, since its view session is its own.
	notify := func(ctx context.Context, text string) error {
		clients, err := s.execTmux(ctx, sess, "list-clients", "-t", viewSession, "-F", "#{client_name}")
		if err != nil {
			return err
		}
		for _, client := range strings.Fields(string(clients)) {
			if _, err := s.execTmux(ctx, sess, "display-message", "-d", noticeMillis, "-c", client, "--", buildLiteralTmuxFormat(text)); err != nil {
				return err
			}
		}
		return nil
	}
	bridge(ctx, ws, proc, logger, detach, notify, view.setVisible)

	// Final write on disconnect. Use a fresh, non-cancelled context
	// so the row update lands even when the request context has
	// already been cancelled by the client closing the WS.
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClose()
	// A terminal that went out of view already recorded when it did.
	if !view.isVisible() {
		return
	}
	if rerr := s.RecordActivity(closeCtx, id); rerr != nil {
		logger.Warn("attach: record activity on close", "err", rerr)
	}
}

// sessionView is one attach's part in whether its session is being looked at. It counts while its
// terminal is on screen. A terminal with no tabs is always on screen.
type sessionView struct {
	// service is the session Service that is handling this attach, the receiver of handleAttach. It
	// holds the count of each session's viewers and writes last_activity_at to the database, so the
	// view needs it to count itself as a viewer and to record when it was last on screen.
	service *Service
	id      string

	mu sync.Mutex
	// untrack is nil while the terminal is out of view.
	untrack func()
	// isEnded is set once the attach is over, since its reader can still be handling a frame.
	isEnded bool
}

// setVisible counts the session as viewed only while its terminal is on screen. A change records
// activity, since coming into view is looking at the session and going out of view is the last moment
// you did. Going out of view records first, so no moment reads as unviewed with the old time.
func (v *sessionView) setVisible(ctx context.Context, visible bool) error {
	v.mu.Lock()
	isChanged := !v.isEnded && visible != (v.untrack != nil)
	if isChanged && visible {
		v.untrack = v.service.trackCurrentSessionViewers(v.id)
	}
	v.mu.Unlock()
	if !isChanged {
		return nil
	}
	err := v.service.RecordActivity(ctx, v.id)
	if !visible {
		v.mu.Lock()
		v.stopCountingLocked()
		v.mu.Unlock()
	}
	return err
}

func (v *sessionView) isVisible() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.untrack != nil
}

func (v *sessionView) end() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.isEnded = true
	v.stopCountingLocked()
}

func (v *sessionView) stopCountingLocked() {
	if v.untrack != nil {
		v.untrack()
		v.untrack = nil
	}
}

// bridge connects a WebSocket to a sandbox.Process. Three concerns
// run concurrently:
//
//  1. proc.Stdout → ws.Write(binary): everything the tty wants to
//     show the user.
//  2. ws.Read → proc.Stdin().Write: everything the user types.
//     Text frames are decoded as JSON control messages.
//  3. proc.Wait → close ws cleanly when the inner process exits.
//
// The first goroutine to error or signal end-of-stream tears down
// the whole bridge via ctx cancellation.
func bridge(ctx context.Context, ws *websocket.Conn, proc sandbox.Process, logger *slog.Logger, detach func(context.Context) error, notify func(context.Context, string) error, setVisible func(context.Context, bool) error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdin := proc.Stdin()
	defer func() { _ = stdin.Close() }()

	// proc.Stdout → ws.binary
	//
	// Backpressure: ws.Write blocks until the WS socket accepts the
	// frame. A slow / paused client lets the OS socket buffer fill,
	// which then back-pressures into the PTY read (the next Read
	// blocks because the kernel-side pipe is full). That's the
	// behavior we want: the PTY pauses output rather than us
	// buffering unbounded in user space. If we ever want to drop
	// frames instead of pausing the PTY, this is the spot to add a
	// SetWriteDeadline equivalent.
	go func() {
		defer cancel()
		buf := make([]byte, 8192)
		for {
			n, err := proc.Stdout().Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ws → proc.Stdin (and JSON control frames)
	go func() {
		defer cancel()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, werr := stdin.Write(data); werr != nil {
					return
				}
			case websocket.MessageText:
				var msg AttachControlMessage
				if jerr := json.Unmarshal(data, &msg); jerr != nil {
					logger.Warn("attach: bad control frame", "err", jerr)
					continue
				}
				switch msg.Type {
				case "resize":
					if rerr := proc.Resize(msg.Cols, msg.Rows); rerr != nil {
						logger.Warn("attach: resize failed", "err", rerr)
					}
				case "detach":
					if derr := detach(ctx); derr != nil {
						logger.Warn("attach: detach failed", "err", derr)
					}
				case "notice":
					if err := notify(ctx, msg.Text); err != nil {
						logger.Warn("attach: notice failed", "err", err)
					}
				case "visibility":
					if err := setVisible(ctx, msg.Visible); err != nil {
						logger.Warn("attach: record activity on a visibility change", "err", err)
					}
				default:
					logger.Warn("attach: unknown control type", "type", msg.Type)
				}
			}
		}
	}()

	// Block until any goroutine signals end-of-stream (via cancel)
	// or the request context is cancelled by the caller.
	<-ctx.Done()
	_ = ws.Close(websocket.StatusNormalClosure, "")
}

// attachStream is one live terminal stream, tracked only so a stop can end it.
type attachStream struct{ cancel context.CancelFunc }

// beginAttach registers a terminal stream.
//
//	ctx      ends when the request does or when the node starts shutting down
//	release  call when the stream and its cleanup are done
//	ok       false once a stop has begun, so no new stream starts against runtimes that are going away
func (s *Service) beginAttach(parent context.Context) (ctx context.Context, release func(), ok bool) {
	ctx, cancel := context.WithCancel(parent)
	stream := &attachStream{cancel: cancel}

	s.attached.mu.Lock()
	if s.attached.closing {
		s.attached.mu.Unlock()
		cancel()
		return nil, nil, false
	}
	if s.attached.streams == nil {
		s.attached.streams = make(map[*attachStream]struct{})
	}
	s.attached.streams[stream] = struct{}{}
	s.attached.wg.Add(1)
	s.attached.mu.Unlock()

	return ctx, func() {
		s.attached.mu.Lock()
		delete(s.attached.streams, stream)
		s.attached.mu.Unlock()
		cancel()
		s.attached.wg.Done()
	}, true
}

// CloseAttachStreams ends every terminal stream and waits for them to finish. Called on shutdown
// before the runtimes and the database close, so each stream's cleanup (killing its tmux session, the
// last activity write) still has something to talk to.
func (s *Service) CloseAttachStreams(timeout time.Duration) {
	s.attached.mu.Lock()
	s.attached.closing = true
	live := len(s.attached.streams)
	for stream := range s.attached.streams {
		stream.cancel()
	}
	s.attached.mu.Unlock()
	if live == 0 {
		return
	}

	s.logger.Info("closing terminal streams", "streams", live)
	done := make(chan struct{})
	go func() {
		s.attached.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		s.logger.Warn("gave up waiting for terminal streams to close", "streams", live)
	}
}

// buildLiteralTmuxFormat escapes text so tmux shows it as is. tmux expands # in a message, and #() would
// run a command.
func buildLiteralTmuxFormat(text string) string {
	return strings.ReplaceAll(text, "#", "##")
}
