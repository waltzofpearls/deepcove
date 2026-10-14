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
	// stopWatching ends the watch for networks the host joins, which the VM's guard blocks.
	stopWatching func()
}

// Stop tears down the VM, then the helpers that were serving it, then the directory they used. The watch
// for networks the host joins lasts until the VM is gone, since until then the guest can still use one.
func (m *machine) Stop() error {
	vmErr := m.process.Stop()
	m.stopWatching()
	return errors.Join(
		vmErr,
		stopAll(m.helpers),
		os.RemoveAll(m.ipcDir),
	)
}
