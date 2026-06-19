//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// resizeNotify calls onResize with the new terminal size on every SIGWINCH, until ctx is done.
func resizeNotify(ctx context.Context, fd int, onResize func(cols, rows uint16)) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-winch:
			if cols, rows, err := term.GetSize(fd); err == nil {
				onResize(uint16(cols), uint16(rows))
			}
		}
	}
}
