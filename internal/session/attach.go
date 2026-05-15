package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

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

	proc, err := s.sandbox.Exec(ctx, sandbox.Handle(sess.ContainerID), sandbox.ExecSpec{
		Cmd: []string{"tmux", "attach", "-t", "main"},
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

	bridge(ctx, ws, proc, s.logger.With("session_id", id))
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
