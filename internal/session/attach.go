package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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

// AttachControlMessage is the JSON shape of WS text frames the
// client may send. Only "resize" is defined today; new types can be
// added without breaking older clients (unknown types are ignored).
type AttachControlMessage struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// handleAttach upgrades to a WebSocket and bridges it to a
// `tmux attach -t main` exec inside the session's sandbox. Binary
// frames are bytes flowing both ways (terminal I/O); text frames
// are JSON control messages (resize today).
//
// Optional ?tmux_window= query param attaches the client to a
// specific tmux window. tmux tracks "current window" per-client,
// so two simultaneous attaches with different ?tmux_window= values
// each see their own window — the substrate the upcoming Split
// mode (one session, two side-by-side terminals on different
// windows) is built on. Absent or empty → legacy behavior (follow
// the session's globally-current window).
func (s *Service) handleAttach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sess.Status != StatusRunning {
		writeError(w, http.StatusConflict, "session is not running")
		return
	}

	// Parse + validate optional window targeting BEFORE upgrading
	// the WS so an invalid name fails as a clean 400 instead of an
	// awkward post-upgrade close. Uses the SYNTAX-only validator —
	// the stricter validateTmuxWindowName forbids "main" because no
	// caller should *create* a window with that name (it'd collide
	// with the agent's window), but here we WANT to allow attaching
	// to "main" since that's the agent's window itself.
	tmuxWindow := r.URL.Query().Get("tmux_window")
	if tmuxWindow != "" {
		if err := validateTmuxWindowNameSyntax(tmuxWindow); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Each attach runs in its own per-connection tmux session,
	// created as a member of the "main" session group. Why: tmux's
	// `select-window` operates on the SESSION's active-window
	// state, not per-client — so two simultaneous attaches with
	// different ?tmux_window= values would clobber each other,
	// and Split mode (two attaches in parallel) would show the
	// same window on both sides.
	//
	// Session-group members share the linked window list (so the
	// windows API still operates on "main" canonically), but each
	// member has its own current-window — true per-client window
	// views. Killing the view session on disconnect (deferred
	// below) also tears down the attached tmux client process,
	// fixing the separate bug where Docker exec doesn't reliably
	// SIGHUP the child on connection close (left tmux client
	// processes piling up inside the container after each toggle).
	viewSession := "view-" + uuid.NewString()[:8]
	setupCtx, cancelSetup := context.WithTimeout(r.Context(), 3*time.Second)
	if _, err := s.execTmux(setupCtx, sess.ContainerID,
		"new-session", "-d", "-s", viewSession, "-t", AgentTmuxWindowName,
	); err != nil {
		cancelSetup()
		s.logger.Warn("attach: view session setup failed", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "view session setup: "+err.Error())
		return
	}
	cancelSetup()
	defer func() {
		// Cleanup on every return path. Killing the view session
		// detaches the inside-container tmux client and the exec
		// process exits, preventing the accumulation.
		killCtx, cancelKill := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelKill()
		if _, err := s.execTmux(killCtx, sess.ContainerID, "kill-session", "-t", viewSession); err != nil {
			s.logger.Warn("attach: view session cleanup failed",
				"id", id, "view", viewSession, "err", err)
		}
	}()

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{AttachSubprotocol},
		// We deliberately do not check Origin here — this listener
		// is local TCP, not exposed to the network. Once tsnet
		// listener lands the policy gets stricter.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logger.Warn("attach: ws accept failed", "id", id, "err", err)
		return
	}
	// Drop close-frame deadline so a hung peer doesn't block other
	// streams forever. We rely on the request context for
	// shutdown propagation.
	ctx := r.Context()
	defer ws.CloseNow()

	// `-u` forces UTF-8 client mode. Without it, tmux substitutes non-ASCII bytes with
	// `_` when sending to clients (Alpine has no locale set, so tmux defaults to 8-bit).
	cmd := []string{"tmux", "-u", "attach", "-t", viewSession}
	if tmuxWindow != "" {
		cmd = append(cmd, ";", "select-window", "-t", viewSession+":"+tmuxWindow)
	}
	proc, err := s.sandbox.Exec(ctx, sandbox.Handle(sess.ContainerID), sandbox.ExecSpec{
		Cmd: cmd,
		Tty: true,
		// TERM is required for tmux to function — without it tmux
		// can run but doesn't recognise the terminal type and
		// some inputs/escape sequences misbehave.
		Env: map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, "exec failed: "+err.Error())
		return
	}
	defer proc.Close()

	// Record activity on attach. Idle ticker keeps last_activity_at
	// fresh for the canvas sort; one final write on disconnect
	// captures the exact moment the user walked away. All three
	// writes are best-effort — a DB hiccup shouldn't kill the
	// terminal session.
	logger := s.logger.With("session_id", id)
	if rerr := s.RecordActivity(ctx, id); rerr != nil {
		logger.Warn("attach: record activity on open", "err", rerr)
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
				if rerr := s.RecordActivity(tickerCtx, id); rerr != nil {
					logger.Warn("attach: record activity tick", "err", rerr)
				}
			}
		}
	}()

	bridge(ctx, ws, proc, logger)

	// Final write on disconnect. Use a fresh, non-cancelled context
	// so the row update lands even when the request context has
	// already been cancelled by the client closing the WS.
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClose()
	if rerr := s.RecordActivity(closeCtx, id); rerr != nil {
		logger.Warn("attach: record activity on close", "err", rerr)
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
func bridge(ctx context.Context, ws *websocket.Conn, proc sandbox.Process, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdin := proc.Stdin()
	defer stdin.Close()

	// proc.Stdout → ws.binary
	//
	// Backpressure: ws.Write blocks until the WS socket accepts the
	// frame. A slow / paused client lets the OS socket buffer fill,
	// which then back-pressures into the PTY read (the next Read
	// blocks because the kernel-side pipe is full). That's the
	// behavior we want — the PTY pauses output rather than us
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
