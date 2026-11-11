package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/coder/websocket"
	"golang.org/x/term"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// detachKey is Ctrl+\. Many terminals send it for Ctrl+4 too, which helps keyboards where \ needs AltGr.
const detachKey = 0x1c

// maxInputPiece is the most input connect sends in one message, the same as one read from the terminal.
const maxInputPiece = 4096

// maxNoticeBytes keeps a notice far below the attach server's 32 KiB message limit, even once JSON
// escapes it. A status line shows much less anyway.
const maxNoticeBytes = 1024

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

	// Both must be a TTY, also so a stalled pipe on stdout cannot block the output after a detach.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("`connect` requires a terminal on stdin and stdout")
	}

	client := newAPIClient(cfg)
	sess, err := resolveSession(ctx, client, target)
	if errors.Is(err, huh.ErrUserAborted) {
		return nil
	}
	if err != nil {
		return err
	}
	if sess.Status != session.StatusRunning {
		return fmt.Errorf("session %s is %s, not running", sess.Name, sess.Status)
	}

	// Prefer the local control socket, falling back to TCP. The "unix" host is a placeholder,
	// socketHTTPClient dials the socket regardless of the URL host.
	dialOpts := &websocket.DialOptions{Subprotocols: []string{session.AttachSubprotocol}}
	// Name the node that listed the session, the same as an upload does, rather than leave it to a guess.
	if sess.Host != nil && sess.Host.StableNodeID != "" {
		dialOpts.HTTPHeader = http.Header{session.NodeHeader: []string{sess.Host.StableNodeID}}
	}
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
		reads := make(chan []byte)
		go readTerminal(bridgeCtx, os.Stdin, reads)
		var pastes pasteCollector
		upload := func(ctx context.Context, localPath string) (string, error) {
			return uploadToSession(ctx, client, sess, localPath)
		}
		// While a pasted file uploads, the user may keep typing. Those keys are saved here and sent once the
		// upload is done, so they land after the pasted path instead of before it.
		var keysTypedDuringUpload []inputPart
		for {
			var parts []inputPart
			if len(keysTypedDuringUpload) > 0 {
				parts, keysTypedDuringUpload = keysTypedDuringUpload, nil
			} else {
				// A terminal sends a paste in one go, so a paste that stops short is let go as typing.
				var pasteTimeout <-chan time.Time
				if pastes.isPasteInProgress {
					pasteTimeout = time.After(pasteWait)
				}
				select {
				case <-bridgeCtx.Done():
					return
				case read, isOpen := <-reads:
					if !isOpen {
						return
					}
					parts = pastes.collect(read)
				case <-pasteTimeout:
					parts = pastes.flush()
				}
			}
			for index, part := range parts {
				if part.isPossiblyFilePaste {
					// A Ctrl+\ already typed after this paste means the user is leaving, so skip the upload.
					if hasDetachKey(parts[index+1:]) || hasDetachKey(keysTypedDuringUpload) {
						continue
					}
					paste, keys, err := uploadWhileReading(bridgeCtx, part.data, reads, &pastes, upload)
					keysTypedDuringUpload = append(keysTypedDuringUpload, keys...)
					if err != nil {
						_ = sendNotice(bridgeCtx, ws, "deepcove: "+err.Error())
					}
					if err := writeInput(bridgeCtx, ws, paste); err != nil {
						return
					}
					continue
				}
				if part.isConsideredTextPaste {
					if err := writeInput(bridgeCtx, ws, part.data); err != nil {
						return
					}
					continue
				}
				input, isDetach := findDetachKey(part.data)
				if len(input) > 0 {
					if err := writeInput(bridgeCtx, ws, input); err != nil {
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
				if _, err := os.Stdout.Write(data); err != nil {
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

// writeInput sends input in pieces the attach server accepts, since it reads at most 32 KiB per message.
func writeInput(ctx context.Context, ws *websocket.Conn, input []byte) error {
	for _, piece := range buildInputPieces(input) {
		if err := ws.Write(ctx, websocket.MessageBinary, piece); err != nil {
			return err
		}
	}
	return nil
}

// buildInputPieces splits input into pieces of at most maxInputPiece, in order.
func buildInputPieces(input []byte) [][]byte {
	var pieces [][]byte
	for len(input) > 0 {
		size := min(len(input), maxInputPiece)
		pieces = append(pieces, input[:size])
		input = input[size:]
	}
	return pieces
}

// hasDetachKey reports whether Ctrl+\ was typed in parts. A paste can hold the same byte, but that is
// pasted text and not a key press.
func hasDetachKey(parts []inputPart) bool {
	for _, part := range parts {
		if !part.isPossiblyFilePaste && !part.isConsideredTextPaste && bytes.IndexByte(part.data, detachKey) >= 0 {
			return true
		}
	}
	return false
}

// findDetachKey returns the input before the detach key, and whether the key was pressed.
func findDetachKey(input []byte) ([]byte, bool) {
	if index := bytes.IndexByte(input, detachKey); index >= 0 {
		return input[:index], true
	}
	return input, false
}

// resolveSession looks up a session on any of your nodes by id, name or id prefix. When a name or prefix
// matches more than one session, it asks which one in a menu.
func resolveSession(ctx context.Context, client *apiClient, target string) (*session.Session, error) {
	listed, err := client.ListEveryNode(ctx, true /* includeAll */)
	if err != nil {
		return nil, err
	}
	matches, title := findSessionMatches(listed.Sessions, target)
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no session matching %q", target)
	case 1:
		return matches[0], nil
	default:
		return pickSession(ctx, title, matches)
	}
}

// findSessionMatches returns the sessions target names, and a menu title when there is more than one.
// A full id wins, then names, then an id prefix of at least 4 characters.
func findSessionMatches(sessions []*session.Session, target string) ([]*session.Session, string) {
	var named, prefixed []*session.Session
	for _, s := range sessions {
		if s.ID == target {
			return []*session.Session{s}, ""
		}
		if s.Name == target {
			named = append(named, s)
		}
		if len(target) >= 4 && strings.HasPrefix(s.ID, target) {
			prefixed = append(prefixed, s)
		}
	}
	matches, title := prefixed, fmt.Sprintf("%d sessions start with %q. Which one?", len(prefixed), target)
	if len(named) > 0 {
		matches, title = named, fmt.Sprintf("%d sessions are named %q. Which one?", len(named), target)
	}
	if len(matches) < 2 {
		title = ""
	}
	return matches, title
}

// pickSession shows the matches as rows like `deepcove ls` prints, picked with the arrow keys.
func pickSession(ctx context.Context, title string, matches []*session.Session) (*session.Session, error) {
	header, rows := buildSessionRows(matches)
	options := make([]huh.Option[*session.Session], 0, len(matches))
	for index, match := range matches {
		options = append(options, huh.NewOption(rows[index], match))
	}
	var picked *session.Session
	// The header goes in the description, indented past the "> " in front of the rows.
	menu := huh.NewSelect[*session.Session]().Title(title).Description("  " + header).Options(options...).Value(&picked)
	if err := huh.NewForm(huh.NewGroup(menu)).WithTheme(huh.ThemeFunc(buildMenuTheme)).RunWithContext(ctx); err != nil {
		return nil, err
	}
	return picked, nil
}

// buildMenuTheme is huh's default theme with the arrow and the picked row in the web UI's brand teal.
func buildMenuTheme(isDark bool) *huh.Styles {
	styles := huh.ThemeCharm(isDark)
	teal := lipgloss.LightDark(isDark)(lipgloss.Color("#009d89"), lipgloss.Color("#00aa95"))
	styles.Focused.SelectSelector = styles.Focused.SelectSelector.Foreground(teal)
	styles.Focused.SelectedOption = styles.Focused.SelectedOption.Foreground(teal)
	return styles
}

// buildSessionRows lines up a header and each session's id, name, node, agent, status and age into columns.
func buildSessionRows(sessions []*session.Session) (string, []string) {
	ids := buildUniqueIDs(sessions)
	var table strings.Builder
	columns := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(columns, "ID\tNAME\tNODE\tAGENT\tSTATUS\tAGE")
	for index, s := range sessions {
		_, _ = fmt.Fprintf(columns, "%s\t%s\t%s\t%s\t%s\t%s\n", ids[index], buildOneLineName(s.Name), nodeName(s), s.Agent, s.Status, ageOf(s.CreatedAt))
	}
	_ = columns.Flush()
	lines := strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n")
	return lines[0], lines[1:]
}

// buildOneLineName shows a tab, newline or other control character in a name as an escape like \n, so a
// session always stays on one row of the menu.
func buildOneLineName(name string) string {
	var line strings.Builder
	for _, char := range name {
		if unicode.IsControl(char) {
			quoted := strconv.QuoteRune(char)
			line.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		line.WriteRune(char)
	}
	return line.String()
}

// buildUniqueIDs cuts the ids as short as `deepcove ls` does, or longer when that would make two match.
func buildUniqueIDs(sessions []*session.Session) []string {
	longest := 0
	for _, s := range sessions {
		longest = max(longest, len(s.ID))
	}
	ids := make([]string, len(sessions))
	for size := min(shortIDLength, longest); ; size++ {
		seen := make(map[string]bool, len(sessions))
		isUnique := true
		for index, s := range sessions {
			ids[index] = s.ID[:min(size, len(s.ID))]
			isUnique = isUnique && !seen[ids[index]]
			seen[ids[index]] = true
		}
		if isUnique || size >= longest {
			return ids
		}
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

// sendNotice asks the server to show text in this terminal's tmux status line, since tmux owns the screen.
func sendNotice(ctx context.Context, ws *websocket.Conn, text string) error {
	body, _ := json.Marshal(session.AttachControlMessage{Type: "notice", Text: buildShortNotice(text)})
	return ws.Write(ctx, websocket.MessageText, body)
}

// buildShortNotice cuts text to maxNoticeBytes on a character boundary, and ends it with "..." when cut.
func buildShortNotice(text string) string {
	if len(text) <= maxNoticeBytes {
		return text
	}
	cut := maxNoticeBytes - len("...")
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "..."
}
