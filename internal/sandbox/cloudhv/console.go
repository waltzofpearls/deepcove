package cloudhv

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openConsoleFIFO makes the guest console a pipe, the way Apple VF hands one over, so the vm package
// drains one kind of stream. Cloud Hypervisor's --console only writes to a file, and a FIFO is a file it
// can open.
//
// O_RDWR because opening a FIFO read only blocks until a writer arrives, and O_NONBLOCK so Go polls the
// fd on Linux, where this runs. macOS never polls a FIFO opened as a file, so a read there is not
// interruptible.
func openConsoleFIFO(path string) (*os.File, error) {
	_ = removeSocket(path)
	if err := unix.Mkfifo(path, 0o600); err != nil {
		return nil, fmt.Errorf("cloudhv: create the console fifo %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("cloudhv: open the console fifo %s: %w", path, err)
	}
	return file, nil
}
