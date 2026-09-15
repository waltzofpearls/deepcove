// Package cloudhv boots session VMs with Cloud Hypervisor. It is the Linux backend for the vm package,
// which owns everything above the hypervisor.
//
// A VM is four processes: cloud-hypervisor itself, one virtiofsd per shared directory, and passt for the
// network. They talk over UNIX sockets in a per VM run directory, which is why that directory has to
// stay short enough for a socket path.
//
// Those sockets carry the guest's credential and config services, so the directory's 0700 mode is what
// protects them. That is enough because anyone with this uid can read the credential store on disk
// anyway; it is not enough for a machine where another user is a threat, and that machine needs a
// different answer than a socket permission.
package cloudhv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
	"github.com/waltzofpearls/deepcove/internal/vmruntime"
)

// kvmDevice is what a host needs to run VMs at all.
const kvmDevice = "/dev/kvm"

// versionTimeout bounds asking the hypervisor who it is.
const versionTimeout = 5 * time.Second

// Backend is the Cloud Hypervisor hypervisor. Unlike the Apple VF backend it holds configuration: the
// host binaries it drives, and where their sockets live.
type Backend struct {
	// CloudHypervisor, Virtiofsd and Passt are the artifacts a release ships.
	CloudHypervisor string
	Virtiofsd       string
	Passt           string
	// IPCDir holds one directory per running VM, each with the sockets and the console FIFO its
	// processes talk over. Keep it short: a socket path has a length limit.
	IPCDir string
}

// Boot starts a VM and the helpers it needs, in the order they have to come up: the file shares and the
// network first, because the VMM connects to them as it starts.
func (b Backend) Boot(spec vm.Spec) (vm.Machine, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	ipcDir, err := b.newIPCDir()
	if err != nil {
		return nil, err
	}

	p := paths{
		api:     filepath.Join(ipcDir, "api.sock"),
		vsock:   filepath.Join(ipcDir, "vsock.sock"),
		console: filepath.Join(ipcDir, "console"),
	}
	var helpers []*child
	fail := func(err error) (vm.Machine, error) {
		return nil, errors.Join(err, stopAll(helpers), os.RemoveAll(ipcDir))
	}

	for i, share := range spec.Shares {
		socket := filepath.Join(ipcDir, fmt.Sprintf("fs%d.sock", i))
		helper, err := startVirtiofsd(b.Virtiofsd, socket, share)
		if err != nil {
			return fail(err)
		}
		helpers = append(helpers, helper)
		p.fsSockets = append(p.fsSockets, socket)
	}
	if b.Passt != "" {
		socket := filepath.Join(ipcDir, "net.sock")
		helper, err := startPasst(b.Passt, socket, passtRunAsArgs())
		if err != nil {
			return fail(err)
		}
		helpers = append(helpers, helper)
		p.netSocket = socket
	}

	proc, err := launch(b.CloudHypervisor, spec, p)
	if err != nil {
		return fail(err)
	}
	return &machine{process: proc, helpers: helpers, ipcDir: ipcDir}, nil
}

// RequireKVM reports whether this host can run VMs at all.
func RequireKVM() error {
	if _, err := os.Stat(kvmDevice); err != nil {
		return fmt.Errorf("cloudhv: %s is missing, so this host cannot run VMs: %w", kvmDevice, sandbox.ErrUnavailable)
	}
	if err := unix.Access(kvmDevice, unix.R_OK|unix.W_OK); err != nil {
		return fmt.Errorf("cloudhv: %s is not writable by this user: %w", kvmDevice, sandbox.ErrUnavailable)
	}
	return nil
}

// Version reports whether this host can run VMs, and who would run them.
func (b Backend) Version() (string, error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	if err := RequireKVM(); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, b.CloudHypervisor, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("cloudhv: run %s --version: %w", b.CloudHypervisor, err)
	}
	// It answers over two lines, and the second is a migration protocol number.
	version, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if version == "" {
		version = "cloud-hypervisor " + vmruntime.CloudHypervisorVersion
	}
	return version, nil
}

func (b Backend) validate() error {
	for name, path := range map[string]string{
		"cloud-hypervisor": b.CloudHypervisor,
		"virtiofsd":        b.Virtiofsd,
		"ipc directory":    b.IPCDir,
	} {
		if path == "" {
			return fmt.Errorf("cloudhv: no %s configured: %w", name, sandbox.ErrNotSupported)
		}
	}
	return nil
}

// newIPCDir makes one VM's directory. The name is short on purpose: every socket in it counts against
// the path limit.
func (b Backend) newIPCDir() (string, error) {
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	dir := filepath.Join(b.IPCDir, hex.EncodeToString(id[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cloudhv: %w", err)
	}
	return dir, nil
}
