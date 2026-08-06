package applevf

import (
	"archive/tar"
	"context"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// SmokeSpec is the exported input for the applevf-smoke boot harness (cmd/applevf-smoke). It mirrors the
// internal vmSpec but is a stable surface for the dev command; production session boots go through the
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
	// Agent names the credential bundle the guest brokers, if any.
	Agent string
	// OverlayBytes caps the guest's writable layer, the same knob a session's disk resource sets. Zero
	// leaves it at the tmpfs default of half the guest's RAM.
	OverlayBytes int64
	// CredStore, when set, runs the host side credential broker for this VM so the guest can fetch its
	// stored login at boot and hand back a refreshed one at shutdown.
	CredStore credbroker.Store
}

// SmokeMachine is the running VM handle the smoke harness drives: read/write the console, run a command
// inside the guest over vsock, then stop.
type SmokeMachine interface {
	Console() io.ReadWriteCloser
	Exec(ctx context.Context, req guestexec.ExecRequest) (sandbox.Process, error)
	Stop() error
}

// BootSmoke boots one VM from a SmokeSpec. On non darwin platforms it returns the not supported error
// from bootVM. Running a booted VM requires the com.apple.security.virtualization entitlement.
func BootSmoke(s SmokeSpec) (SmokeMachine, error) {
	cmdline := s.Cmdline
	if cmdline == "" {
		cmdline = defaultCmdline().String()
	}
	m, err := bootVM(vmSpec{
		KernelPath:   s.KernelPath,
		InitrdPath:   s.InitrdPath,
		Cmdline:      cmdline,
		BaseImage:    s.BaseImage,
		ScratchImage: s.ScratchImage,
		CPUCount:     s.CPUCount,
		MemoryBytes:  s.MemoryBytes,
	})
	if err != nil {
		return nil, err // return a nil interface, not a typed nil *machine
	}
	// The guest asks the host for its entrypoint at boot, so serve one before it gets that far.
	if err := serveGuestConfig(m, guestcfg.Config{
		Entrypoint:   s.Entrypoint,
		Agent:        s.Agent,
		OverlayBytes: s.OverlayBytes,
	}); err != nil {
		_ = m.Stop()
		return nil, err
	}
	if s.CredStore != nil {
		if err := serveGuestCredentials(m, s.CredStore); err != nil {
			_ = m.Stop()
			return nil, err
		}
	}
	return m, nil
}

// BuildSmokeImage pulls an OCI reference, flattens it, bakes in the guest init binary and a launch
// script, and writes a read only ext4 base image to outPath. It is the one command form of the image
// pipeline for the boot harness; production goes through imageStore with its digest cache.
func BuildSmokeImage(ctx context.Context, ref, outPath, guestInitPath string) error {
	img, err := pullRemote(ctx, ref, guestPlatform())
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}

	rc := mutate.Extract(img)
	defer func() { _ = rc.Close() }()
	_, err = createRootfs(outPath, tar.NewReader(rc), baseInjection(guestInitPath))
	return err
}
