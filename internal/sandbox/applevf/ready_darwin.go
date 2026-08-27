//go:build darwin

package applevf

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// readyPollInterval is how often to ask the guest whether it is up. Boots take seconds, so polling this
// often costs nothing and keeps session create feeling responsive.
const readyPollInterval = 250 * time.Millisecond

// waitReady blocks until the guest's control service answers, which means the kernel booted, the guest
// init completed its sequence, and the session entrypoint was started.
//
// Without this, Start returns as soon as Virtualization.framework accepts the VM, so a guest that
// panicked still looked like a running session and only revealed itself as an empty terminal.
func (m *machine) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := m.Ping(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("applevf: the guest did not come up within %s: %w%s", timeout, lastErr, m.consoleHint())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPollInterval):
		}
	}
}

// consoleHint appends what the guest last printed, which is usually the actual reason it failed.
func (m *machine) consoleHint() string {
	if m.consoleLog == nil {
		return ""
	}
	tail := strings.TrimSpace(string(m.consoleLog.Tail()))
	if tail == "" {
		return " (the guest printed nothing to its console)"
	}
	// Only the last few lines: the whole boot log would bury the failure.
	lines := strings.Split(tail, "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return "\nlast guest console output:\n" + strings.Join(lines, "\n")
}

// captureConsole starts draining the guest console into a bounded buffer, and into path when one is
// given. It must run for every VM the driver owns: the console is a pipe, so an undrained one eventually
// blocks the guest on a write.
//
// The file is the part that outlives the process. A guest panic or an OOM report is printed there and
// nowhere else.
func (m *machine) captureConsole(path string) {
	if m.consoleLog != nil {
		return
	}
	m.consoleLog = newConsoleBuffer()
	reader := io.Reader(m.console)
	if path != "" {
		if file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			m.closers = append(m.closers, file)
			reader = io.TeeReader(reader, file)
		}
	}
	go m.consoleLog.drain(reader)
}
