//go:build windows

package cli

import (
	"context"
	"time"

	"golang.org/x/term"
)

// resizeNotify polls the terminal size and calls onResize on change, until ctx is done. Windows
// has no SIGWINCH; the console WINDOW_BUFFER_SIZE_EVENT path would need to drain the same input
// buffer we read as raw stdin, so polling is simpler and correct. The initial size is sent by
// the caller, so we seed the last seen size to avoid a redundant first event.
func resizeNotify(ctx context.Context, fd int, onResize func(cols, rows uint16)) {
	lastCols, lastRows, _ := term.GetSize(fd)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cols, rows, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			if cols != lastCols || rows != lastRows {
				lastCols, lastRows = cols, rows
				onResize(uint16(cols), uint16(rows))
			}
		}
	}
}
