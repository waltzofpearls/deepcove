//go:build windows

package cli

import (
	"context"
	"errors"
)

// cmdSessionAttach is unimplemented on Windows for now. The raw-mode
// terminal handling on Windows differs enough from Unix (no
// SIGWINCH, ConPTY console APIs instead of POSIX termios) that it
// gets its own implementation later, alongside the named-pipe
// transport for the local CLI listener.
func cmdSessionAttach(ctx context.Context, args []string) error {
	_ = ctx
	_ = args
	return errors.New("`deepcove session attach` is not yet implemented on Windows")
}
