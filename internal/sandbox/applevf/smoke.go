package applevf

import (
	"context"
	"io"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// SmokeSpec is the exported input for the applevf-smoke boot harness (cmd/applevf-smoke). It mirrors the
// internal vm.Spec but is a stable surface for the dev command; production session boots go through the
// driver, not this.
type SmokeSpec struct {
	KernelPath   string
	InitrdPath   string
	BaseImage    string
	ScratchImage string
	Cmdline      string // empty defaults to the deepcove guest-init boot line
	CPUCount     uint
	MemoryBytes  uint64
	// Entrypoint is what the guest runs once booted, standing in for the entrypoint the session layer
	// would normally supply. Empty falls back to an interactive shell.
	Entrypoint []string
	// OverlayBytes caps the guest's writable layer, the same knob a session's disk resource sets. Zero
	// leaves it at the tmpfs default of half the guest's RAM.
	OverlayBytes int64
}

// SmokeMachine is the running VM handle the smoke harness drives: read/write the console, run a command
// inside the guest over vsock, then stop.
type SmokeMachine interface {
	Console() io.ReadWriteCloser
	Exec(ctx context.Context, req guestexec.ExecRequest) (sandbox.Process, error)
	Stop() error
}

// BootSmoke boots one VM from a SmokeSpec, inside this process: the smoke tool tests Apple VF itself. On
// non darwin platforms it returns the not supported error from bootVM. Running a booted VM requires the
// com.apple.security.virtualization entitlement.
func BootSmoke(s SmokeSpec) (SmokeMachine, error) {
	cmdline := s.Cmdline
	if cmdline == "" {
		cmdline = vm.DefaultCmdline().String()
	}
	booted, err := bootVM(vm.Spec{
		KernelPath:   s.KernelPath,
		InitrdPath:   s.InitrdPath,
		Cmdline:      cmdline,
		BaseImage:    s.BaseImage,
		ScratchImage: s.ScratchImage,
		CPUCount:     s.CPUCount,
		MemoryBytes:  s.MemoryBytes,
	}, nil)
	if err != nil {
		return nil, err // return a nil interface, not a typed nil *machine
	}
	m := vm.NewGuest(booted, nil)
	// The guest asks the host for its entrypoint at boot, so serve one before it gets that far.
	if err := m.ServeConfig(guestcfg.Config{
		Entrypoint:   s.Entrypoint,
		OverlayBytes: s.OverlayBytes,
	}); err != nil {
		_ = m.Stop()
		return nil, err
	}
	return m, nil
}
