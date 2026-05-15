//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
	"golang.org/x/term"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// cmdSessionAttach handles `deepcove session attach <id-or-name>`.
// It dials the local service's WebSocket attach endpoint, puts the
// terminal into raw mode, and bridges stdin/stdout with the tmux
// session inside the container. SIGWINCH propagates as a JSON
// resize control frame.
//
// Unix-only for now (uses syscall.SIGWINCH). The Windows path will
// land alongside the Unix-socket / named-pipe migration.
func cmdSessionAttach(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("session attach", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: deepcove session attach <id-or-name>")
	}
	target := rest[0]

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newSessionClient(cfg)
	sess, err := resolveSession(ctx, client, target)
	if err != nil {
		return err
	}
	if sess.Status != session.StatusRunning {
		return fmt.Errorf("session %s is %s, not running", sess.Name, sess.Status)
	}

	// stdin must be a TTY for any of this to make sense.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("`session attach` requires a terminal on stdin")
	}

	wsURL := "ws://" + cfg.Addr + "/api/sessions/" + sess.ID + "/attach"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{session.AttachSubprotocol},
	})
	if err != nil {
		return fmt.Errorf("dial attach ws: %w (is `deepcove start` running?)", err)
	}
	defer ws.CloseNow()

	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	defer term.Restore(fd, old)

	// Initial resize: send the terminal's current size so tmux fits
	// the host window from the first frame, not just after SIGWINCH.
	if cols, rows, err := term.GetSize(fd); err == nil {
		_ = sendResize(ctx, ws, uint16(cols), uint16(rows))
	}

	// SIGWINCH → JSON resize frame.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		for {
			select {
			case <-bridgeCtx.Done():
				return
			case <-winch:
				cols, rows, err := term.GetSize(fd)
				if err == nil {
					_ = sendResize(bridgeCtx, ws, uint16(cols), uint16(rows))
				}
			}
		}
	}()

	// stdin → ws binary
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := ws.Write(bridgeCtx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ws → stdout
	go func() {
		defer cancel()
		for {
			typ, data, err := ws.Read(bridgeCtx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, werr := os.Stdout.Write(data); werr != nil {
					return
				}
			}
			// Text frames are server-to-client control messages —
			// none defined today, but reserve.
		}
	}()

	<-bridgeCtx.Done()
	return nil
}

// resolveSession looks up a session by id prefix or name. We list
// all sessions and match — fine for personal-scale node counts, and
// avoids needing a dedicated lookup endpoint.
func resolveSession(ctx context.Context, client *sessionClient, target string) (*session.Session, error) {
	all, err := client.List(ctx, true /* includeAll */)
	if err != nil {
		return nil, err
	}
	var matches []*session.Session
	for _, s := range all {
		if s.ID == target || s.Name == target {
			return s, nil
		}
		if len(target) >= 4 && len(s.ID) >= len(target) && s.ID[:len(target)] == target {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no session matching %q", target)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%q is ambiguous; %d sessions match", target, len(matches))
	}
}

func sendResize(ctx context.Context, ws *websocket.Conn, cols, rows uint16) error {
	body, _ := json.Marshal(session.AttachControlMessage{
		Type: "resize",
		Cols: cols,
		Rows: rows,
	})
	return ws.Write(ctx, websocket.MessageText, body)
}
