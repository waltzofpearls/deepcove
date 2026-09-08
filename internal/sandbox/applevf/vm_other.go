//go:build !darwin

package applevf

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// machine is the non darwin stand in. The Apple VF runtime only functions on macOS; this exists so the
// package builds elsewhere, and every method reports the same thing bootVM does.
type machine struct{}

func bootVM(spec vm.Spec, _ *slog.Logger) (*machine, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return nil, errNotMacOS
}

func (m *machine) Dial(context.Context, uint32) (net.Conn, error) {
	return nil, errNotMacOS
}

func (m *machine) PendingDials() int {
	return 0
}

func (m *machine) Listen(uint32) (net.Listener, error) {
	return nil, errNotMacOS
}

func (m *machine) Console() io.ReadWriteCloser {
	return nil
}

func (m *machine) Running() bool {
	return false
}

func (m *machine) Stop() error {
	return nil
}

func (m *machine) WaitStopped(time.Duration) error {
	return nil
}

var errNotMacOS = fmt.Errorf(
	"applevf: the Apple Virtualization.framework runtime requires macOS: %w", sandbox.ErrNotSupported)
