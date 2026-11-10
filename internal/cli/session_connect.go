package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/term"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// detachKey is Ctrl+\. Many terminals send it for Ctrl+4 too, which helps keyboards where \ needs AltGr.
const detachKey = 0x1c

// detachWait is how long connect waits for tmux to detach before it hangs up anyway.
const detachWait = 3 * time.Second

// tmuxScreenReset is what tmux writes when it detaches (tty_stop_tty): back to the normal screen, cursor
// shown, and the mouse, paste, focus, key and theme report modes off.
const tmuxScreenReset = "\x1b[m\x1b[?1l\x1b>\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1005l" +
	"\x1b[?2004l\x1b[?1004l\x1b[>4m\x1b[?2031l\x1b[?1049l"

// cmdSessionConnect handles `deepcove connect <id-or-name>`, also run as `deepcove session connect`. It dials the local service's
// WebSocket attach endpoint (preferring the Unix socket), puts the terminal into raw mode, and
// bridges stdin/stdout with the tmux session inside the sandbox. Terminal-size changes propagate
// as JSON resize control frames via resizeNotify: SIGWINCH on Unix, polling on Windows (no SIGWINCH).
func cmdSessionConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: deepcove connect <id-or-name>")
	}
	target := rest[0]

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client := newAPIClient(cfg)
	sess, err := resolveSession(ctx, client, target)
	if err != nil {
		return err
	}
	if sess.Status != session.StatusRunning {
		return fmt.Errorf("session %s is %s, not running", sess.Name, sess.Status)
	}

	// Both must be a TTY, also so a stalled pipe on stdout cannot block the output after a detach.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("`connect` requires a terminal on stdin and stdout")
	}

	// Prefer the local control socket, falling back to TCP. The "unix" host is a placeholder,
	// socketHTTPClient dials the socket regardless of the URL host.
	dialOpts := &websocket.DialOptions{Subprotocols: []string{session.AttachSubprotocol}}
	attachPath := "/api/sessions/" + sess.ID + "/attach?detach_hint=true"
	wsURL := "ws://" + cfg.Addr + attachPath
	if sock := socketPath(cfg.DataDir); socketAvailable(sock) {
		wsURL = "ws://unix" + attachPath
		dialOpts.HTTPClient = socketHTTPClient(sock)
	}
	ws, _, err := websocket.Dial(ctx, wsURL, dialOpts)
	if err != nil {
		return fmt.Errorf("dial attach ws: %w (is deepcove running? Start it with `%s`)", err, findStartCommand())
	}
	defer func() { _ = ws.CloseNow() }()

	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, old) }()

	// Initial resize: send the terminal's current size so tmux fits the host window from the
	// first frame, not just after the first resize event.
	if cols, rows, err := term.GetSize(fd); err == nil {
		_ = sendResize(ctx, ws, uint16(cols), uint16(rows))
	}

	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Terminal-size changes become JSON resize frames (OS-specific source).
	go resizeNotify(bridgeCtx, fd, func(cols, rows uint16) {
		_ = sendResize(bridgeCtx, ws, cols, rows)
	})

	var isDetached atomic.Bool
	// stdin → ws binary
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				input, isDetach := findDetachKey(buf[:n])
				if len(input) > 0 {
					if werr := ws.Write(bridgeCtx, websocket.MessageBinary, input); werr != nil {
						return
					}
				}
				if isDetach {
					// tmux detaches itself, so it gives the terminal back the screen it had before.
					// Set first, since the server can close the socket before sendDetach returns.
					isDetached.Store(true)
					detachCtx, cancelDetach := context.WithTimeout(bridgeCtx, detachWait)
					_ = sendDetach(detachCtx, ws)
					<-detachCtx.Done()
					cancelDetach()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ws → stdout
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
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
			// Text frames are server-to-client control messages; none defined today, but reserve.
		}
	}()

	<-bridgeCtx.Done()
	if isDetached.Load() {
		// Wait for the last output, so the reset below lands after it.
		<-outputDone
		// tmux sends this itself when it detaches, but that can be lost, and sending it twice is harmless.
		_, _ = os.Stdout.WriteString(tmuxScreenReset)
		_ = term.Restore(fd, old)
		fmt.Printf("Disconnected from %s. It keeps running.\n", sess.Name)
	}
	return nil
}

// findDetachKey returns the input before the detach key, and whether the key was pressed.
func findDetachKey(input []byte) ([]byte, bool) {
	if index := bytes.IndexByte(input, detachKey); index >= 0 {
		return input[:index], true
	}
	return input, false
}

// resolveSession looks up a session on any of your nodes by id prefix or name. We list all sessions and
// match; fine for personal-scale node counts, and avoids needing a dedicated lookup endpoint.
func resolveSession(ctx context.Context, client *apiClient, target string) (*session.Session, error) {
	listed, err := client.ListEveryNode(ctx, true /* includeAll */)
	if err != nil {
		return nil, err
	}
	var matches []*session.Session
	for _, s := range listed.Sessions {
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

func sendDetach(ctx context.Context, ws *websocket.Conn) error {
	body, _ := json.Marshal(session.AttachControlMessage{Type: "detach"})
	return ws.Write(ctx, websocket.MessageText, body)
}
