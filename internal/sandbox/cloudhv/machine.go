package cloudhv

import (
	"errors"
	"os"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vmproc"
)

// machine is one VM: the hypervisor process, the helpers serving it, and the directory they share.
type machine struct {
	*process
	helpers []*vmproc.Process
	ipcDir  string
}

// Stop tears down the VM, then the helpers that were serving it, then the directory they used.
func (m *machine) Stop() error {
	return errors.Join(
		m.process.Stop(),
		stopAll(m.helpers),
		os.RemoveAll(m.ipcDir),
	)
}
