package cloudhv

import (
	"errors"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// startVirtiofsd shares one host directory with the guest. Cloud Hypervisor has no file sharing of its
// own: it connects to this socket, and virtiofsd serves the directory behind it.
//
// `--sandbox none` because the namespace sandbox needs privileges we do not want to require, and the
// isolation that matters is the VM's, not the daemon's.
func startVirtiofsd(binary, socket string, share vm.Share) (*child, error) {
	argv := []string{
		"--socket-path", socket,
		"--shared-dir", share.HostPath,
		"--tag", share.Tag,
		"--sandbox", "none",
		"--cache", "auto",
	}
	if share.ReadOnly {
		argv = append(argv, "--readonly")
	}
	c, err := startChild("virtiofsd for "+share.Tag, binary, argv)
	if err != nil {
		return nil, err
	}
	if err := waitForFile(c, socket, readyWait); err != nil {
		return nil, errors.Join(err, c.Stop())
	}
	return c, nil
}

// startPasst gives the guest a network with no privileges on the host. passt creates the socket and the
// VMM connects to it, which is why the VMM is the client.
func startPasst(binary, socket string, runAs []string) (*child, error) {
	argv := append([]string{"--vhost-user", "--socket", socket, "--foreground", "--quiet"}, runAs...)
	c, err := startChild("passt", binary, argv)
	if err != nil {
		return nil, err
	}
	if err := waitForFile(c, socket, readyWait); err != nil {
		return nil, errors.Join(err, c.Stop())
	}
	return c, nil
}

// stopAll stops helpers in reverse order and reports everything that went wrong.
func stopAll(helpers []*child) error {
	var err error
	for i := len(helpers) - 1; i >= 0; i-- {
		err = errors.Join(err, helpers[i].Stop())
	}
	return err
}
