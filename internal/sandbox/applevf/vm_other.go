//go:build !darwin

package applevf

import (
	"fmt"
	"io"

	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// machine is the non darwin stand in. The Apple VF runtime only functions on macOS; on other platforms
// bootVM fails fast so the package still builds (the Linux CI runner compiles this file, not the vz cgo
// one) without pulling in Virtualization.framework.
type machine struct{}

func bootVM(spec vmSpec) (*machine, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("applevf: the Apple Virtualization.framework runtime requires macOS: %w", sandbox.ErrNotSupported)
}

func (m *machine) Console() io.ReadWriteCloser { return nil }
func (m *machine) Stop() error                 { return nil }

func (m *machine) Exec(guestexec.ExecRequest) (sandbox.Process, error) {
	return nil, fmt.Errorf("applevf: exec requires macOS: %w", sandbox.ErrNotSupported)
}
