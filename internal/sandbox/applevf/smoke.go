package applevf

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
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
	// CredStore, when set, runs the host side credential broker for this VM so the guest can fetch its
	// stored login at boot and hand back a refreshed one at shutdown.
	CredStore credbroker.Store
}

// SmokeMachine is the running VM handle the smoke harness drives: read/write the console, run a command
// inside the guest over vsock, then stop.
type SmokeMachine interface {
	Console() io.ReadWriteCloser
	Exec(req guestexec.ExecRequest) (sandbox.Process, error)
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
func BuildSmokeImage(ctx context.Context, ref, outPath, guestInitPath, launchCmd, agent string) error {
	img, err := pullRemote(ctx, ref, guestPlatform())
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}

	launchFile, err := os.CreateTemp("", "deepcove-launch-*")
	if err != nil {
		return fmt.Errorf("launch script temp: %w", err)
	}
	defer func() { _ = os.Remove(launchFile.Name()) }()
	if _, err := io.WriteString(launchFile, launchCmd+"\n"); err != nil {
		return fmt.Errorf("write launch script: %w", err)
	}
	if err := launchFile.Close(); err != nil {
		return fmt.Errorf("close launch script: %w", err)
	}

	agentFile, err := writeTemp("deepcove-agent-*", agent)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(agentFile.Name()) }()

	rc := mutate.Extract(img)
	defer func() { _ = rc.Close() }()
	_, err = createRootfs(outPath, tar.NewReader(rc), guestInjection(guestInitPath, launchFile.Name(), agentFile.Name()))
	return err
}

// writeTemp puts content in a temp file for injection into the image.
func writeTemp(pattern, content string) (*os.File, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", pattern, err)
	}
	if _, err := io.WriteString(f, content+"\n"); err != nil {
		return nil, fmt.Errorf("write %s: %w", pattern, err)
	}
	return f, f.Close()
}
