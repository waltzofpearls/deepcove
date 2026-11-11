package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/waltzofpearls/deepcove/internal/session"
)

// pasteStart and pasteEnd wrap every paste once bracketed paste is on, which tmux turns on in the terminal.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// maxCollectedPaste is how much of a paste connect holds back to look for file paths. Anything longer is
// text, so it goes on as pasted text without waiting for its end.
const maxCollectedPaste = 64 << 10

// uploadWait bounds the uploads for one paste.
const uploadWait = 60 * time.Second

// pasteWait is how long connect waits for the rest of a paste before it lets what it holds go as typing.
const pasteWait = time.Second

// inputPart is a piece of what the terminal sent, keys or a paste. Only keys can be Ctrl+\.
type inputPart struct {
	data []byte
	// isPossiblyFilePaste is a paste under 64 KiB. Connect waits for all of it, then checks it for file paths.
	isPossiblyFilePaste bool
	// isConsideredTextPaste is a piece of a paste over 64 KiB. Connect sends it on right away, unchecked.
	isConsideredTextPaste bool
}

// pasteCollector holds a paste back until its end marker arrives, since a terminal can send one paste
// over several reads.
type pasteCollector struct {
	pending []byte
	// isPasteInProgress means a paste has started and hasn't ended, whatever its size.
	isPasteInProgress bool
	// isPastingText means the paste in progress went over 64 KiB, so connect sends its pieces straight on.
	isPastingText bool
}

// collect splits one read into keys and whole pastes. A start marker split across two reads is not
// caught, so that paste goes on as it is.
func (c *pasteCollector) collect(input []byte) []inputPart {
	var parts []inputPart
	for len(input) > 0 {
		if !c.isPasteInProgress {
			start := bytes.Index(input, []byte(pasteStart))
			if start < 0 {
				return append(parts, inputPart{data: input})
			}
			if start > 0 {
				parts = append(parts, inputPart{data: input[:start]})
			}
			c.isPasteInProgress = true
			c.pending = nil
			input = input[start:]
		}
		c.pending = append(c.pending, input...)
		searchFrom := len(pasteStart)
		if c.isPastingText {
			searchFrom = 0
		}
		end := bytes.Index(c.pending[searchFrom:], []byte(pasteEnd))
		if end < 0 {
			if c.isPastingText || len(c.pending) > maxCollectedPaste {
				// Keep the last bytes, which may be the start of an end marker split across two reads.
				sent := len(c.pending) - (len(pasteEnd) - 1)
				if sent > 0 {
					parts = append(parts, inputPart{data: c.pending[:sent], isConsideredTextPaste: true})
					c.pending = bytes.Clone(c.pending[sent:])
				}
				c.isPastingText = true
			}
			return parts
		}
		end += searchFrom + len(pasteEnd)
		parts = append(parts, inputPart{data: c.pending[:end], isPossiblyFilePaste: !c.isPastingText, isConsideredTextPaste: c.isPastingText})
		input = c.pending[end:]
		c.isPasteInProgress = false
		c.isPastingText = false
	}
	return parts
}

// flush lets go of a paste whose end marker never came, as typing, so keys like Ctrl+\ work again.
func (c *pasteCollector) flush() []inputPart {
	if !c.isPasteInProgress {
		return nil
	}
	parts := []inputPart{{data: c.pending}}
	c.pending = nil
	c.isPasteInProgress = false
	c.isPastingText = false
	return parts
}

// readTerminal hands each read from the terminal to reads, until reading fails or ctx ends. It can stay
// in a Read after connect is done, which ends when the command exits.
func readTerminal(ctx context.Context, terminal io.Reader, reads chan<- []byte) {
	defer close(reads)
	buf := make([]byte, 4096)
	for {
		n, err := terminal.Read(buf)
		if n > 0 {
			select {
			case reads <- bytes.Clone(buf[:n]):
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// uploadWhileReading uploads a paste and keeps reading the terminal meanwhile, splitting what comes in
// into keys and pastes. It returns that so the caller sends it after the paste. Ctrl+\ cancels the upload
// instead of waiting it out, with no error since the user is leaving.
func uploadWhileReading(ctx context.Context, paste []byte, reads <-chan []byte, pastes *pasteCollector, upload func(context.Context, string) (string, error)) ([]byte, []inputPart, error) {
	uploadCtx, cancelUpload := context.WithTimeout(ctx, uploadWait)
	defer cancelUpload()
	type uploadResult struct {
		paste []byte
		err   error
	}
	done := make(chan uploadResult, 1)
	go func() {
		uploaded, err := buildUploadedPaste(uploadCtx, paste, upload)
		done <- uploadResult{paste: uploaded, err: err}
	}()
	var typed []inputPart
	isDetachPressed := false
	for {
		var parts []inputPart
		var pasteTimeout <-chan time.Time
		if pastes.isPasteInProgress {
			pasteTimeout = time.After(pasteWait)
		}
		select {
		case result := <-done:
			if isDetachPressed {
				return nil, typed, nil
			}
			return result.paste, typed, result.err
		case read, isOpen := <-reads:
			if !isOpen {
				// The reads are over, and the caller sees that on its next read.
				reads = nil
				continue
			}
			parts = pastes.collect(read)
		case <-pasteTimeout:
			parts = pastes.flush()
		}
		typed = append(typed, parts...)
		if hasDetachKey(parts) {
			isDetachPressed = true
			cancelUpload()
		}
	}
}

// buildUploadedPaste swaps a paste of local file paths, which is what a terminal sends for a dropped or
// copied file, for the paths of uploaded copies in the session. A file that fails to upload is left out
// and named in the error. Anything else comes back as it was.
func buildUploadedPaste(ctx context.Context, paste []byte, upload func(context.Context, string) (string, error)) ([]byte, error) {
	localPaths := findLocalFilePaths(string(paste[len(pasteStart) : len(paste)-len(pasteEnd)]))
	if len(localPaths) == 0 {
		return paste, nil
	}
	var sessionPaths, failures []string
	for _, localPath := range localPaths {
		sessionPath, err := upload(ctx, localPath)
		if err != nil {
			failures = append(failures, filepath.Base(localPath)+": "+err.Error())
			continue
		}
		sessionPaths = append(sessionPaths, buildShellEscapedPath(sessionPath))
	}
	var err error
	if len(failures) > 0 {
		err = fmt.Errorf("could not upload %s", strings.Join(failures, ", "))
	}
	if len(sessionPaths) == 0 {
		return nil, err
	}
	// The trailing space matches the web terminal, so the next word does not run into the path.
	return []byte(pasteStart + strings.Join(sessionPaths, " ") + " " + pasteEnd), err
}

// buildShellEscapedPath puts a backslash before anything a shell would split or expand, the way terminals
// escape a dropped file. Paths in a VM never need it, a cache folder on the host might.
func buildShellEscapedPath(path string) string {
	var escaped strings.Builder
	for _, char := range path {
		if !isPlainPathChar(char) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(char)
	}
	return escaped.String()
}

// isPlainPathChar reports whether a shell takes char as it is. A shell only splits and expands on ASCII,
// which is also why terminals leave a narrow space in a screenshot name alone.
func isPlainPathChar(char rune) bool {
	return 'a' <= char && char <= 'z' || 'A' <= char && char <= 'Z' || '0' <= char && char <= '9' ||
		char > unicode.MaxASCII || strings.ContainsRune("/._-+,:@%=", char)
}

// findLocalFilePaths returns the pasted words when every one is an absolute path to a file on this
// machine, and nil otherwise, so ordinary text is never touched.
func findLocalFilePaths(text string) []string {
	words := splitPastedWords(text)
	for _, word := range words {
		if !filepath.IsAbs(word) {
			return nil
		}
		if info, err := os.Stat(word); err != nil || !info.Mode().IsRegular() {
			return nil
		}
	}
	return words
}

// splitPastedWords splits a paste the way a shell would, so a backslash or quotes keep a path with spaces
// whole. Terminals join dropped files with spaces or newlines. On Windows a backslash is a separator. A
// paste with an unfinished quote or escape is not a list of paths, so it gives nil.
func splitPastedWords(text string) []string {
	var words []string
	var word strings.Builder
	isInWord := false
	isEscaped := false
	isEscapedInDoubleQuotes := false
	var quote rune
	for _, char := range text {
		switch {
		case isEscaped:
			word.WriteRune(char)
			isEscaped = false
		case isEscapedInDoubleQuotes:
			// In double quotes a shell drops the backslash only before " \ $ or `, and keeps it otherwise.
			if !strings.ContainsRune("\"\\$`", char) {
				word.WriteRune('\\')
			}
			word.WriteRune(char)
			isEscapedInDoubleQuotes = false
		case quote != 0 && char == quote:
			quote = 0
		case quote == '"' && char == '\\' && runtime.GOOS != "windows":
			isEscapedInDoubleQuotes = true
		case quote != 0:
			word.WriteRune(char)
		case char == '\\' && runtime.GOOS != "windows":
			isEscaped = true
			isInWord = true
		case char == '\'' || char == '"':
			quote = char
			isInWord = true
		case char == ' ' || char == '\t' || char == '\n' || char == '\r':
			if isInWord {
				words = append(words, word.String())
				word.Reset()
				isInWord = false
			}
		default:
			word.WriteRune(char)
			isInWord = true
		}
	}
	if quote != 0 || isEscaped || isEscapedInDoubleQuotes {
		return nil
	}
	if isInWord {
		words = append(words, word.String())
	}
	return words
}

// uploadToSession copies a local file into the session and returns its path there.
func uploadToSession(ctx context.Context, client *apiClient, sess *session.Session, localPath string) (string, error) {
	data, err := readFileToUpload(localPath)
	if err != nil {
		return "", err
	}
	uploaded, err := client.Upload(ctx, sess, filepath.Base(localPath), data)
	if err != nil {
		return "", err
	}
	return uploaded.Path, nil
}

// readFileToUpload reads a file of at most MaxUploadBytes. The size is checked first so a big video is
// turned away at once, and the read is capped too, since a file can grow in between.
func readFileToUpload(localPath string) ([]byte, error) {
	tooBig := fmt.Errorf("bigger than %d MiB", session.MaxUploadBytes>>20)
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > session.MaxUploadBytes {
		return nil, tooBig
	}
	file, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, session.MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > session.MaxUploadBytes {
		return nil, tooBig
	}
	return data, nil
}
