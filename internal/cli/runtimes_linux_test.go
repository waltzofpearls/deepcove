//go:build linux

package cli

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/sandbox/cloudhv"
	"github.com/waltzofpearls/deepcove/internal/session"
	"github.com/waltzofpearls/deepcove/internal/vmassets"
)

func TestResolveHostBinaries(t *testing.T) {
	all := vmassets.LinuxHost(runtime.GOARCH)
	cases := []struct {
		name    string
		present []vmassets.Artifact
		want    bool
	}{
		{name: "all three downloaded", present: all, want: true},
		{name: "one still missing", present: all[:2], want: false},
		{name: "nothing downloaded", present: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, artifact := range tc.present {
				writeArtifact(t, dir, artifact)
			}
			binaries, ok := resolveHostBinaries(&config.Config{DataDir: dir})
			if ok != tc.want {
				t.Fatalf("got %v, want %v", ok, tc.want)
			}
			if !ok {
				return
			}
			for _, asset := range []vmAsset{binaries.cloudHypervisor, binaries.virtiofsd, binaries.passt} {
				if asset.path == "" || asset.source != sourceRelease {
					t.Errorf("%s: got %q from %q", asset.name, asset.path, asset.source)
				}
			}
		})
	}
}

func writeArtifact(t *testing.T, dataDir string, artifact vmassets.Artifact) {
	t.Helper()
	dir := filepath.Join(dataDir, "vm-artifacts", artifact.Name, artifact.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifact.File), []byte("BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The wiring a Linux node goes through before a VM can boot: what it has, what it is still missing, and
// what it says about each. KVM and the socket directory are answered for it, since no CI machine has one
// and a live node's directory must not be touched.
func TestNewVMRuntime(t *testing.T) {
	cases := []struct {
		name        string
		noKVM       bool
		kernel      bool
		binaries    bool
		wantName    string
		wantMissing string
		wantDriver  bool
	}{
		{name: "a host without kvm offers nothing at all", noKVM: true},
		{
			name: "kvm but nothing downloaded", kernel: false,
			wantName: session.RuntimeCloudHV, wantMissing: session.MissingKernel,
		},
		{
			name: "a kernel but no host binaries", kernel: true,
			wantName: session.RuntimeCloudHV, wantMissing: session.MissingHostBinaries,
		},
		{
			name: "everything in place", kernel: true, binaries: true,
			wantName: session.RuntimeCloudHV, wantDriver: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{DataDir: dir, VMGuestInit: writeStubFile(t, dir, "init")}
			if tc.kernel {
				cfg.VMKernel = writeStubFile(t, dir, "vmlinux")
			}
			if tc.binaries {
				for _, artifact := range vmassets.LinuxHost(runtime.GOARCH) {
					writeArtifact(t, dir, artifact)
				}
			}

			hostCanRunVMs = func() error {
				if tc.noKVM {
					return errors.New("no kvm on this machine")
				}
				return nil
			}
			vmSocketDir = func(string) (string, error) { return t.TempDir(), nil }
			t.Cleanup(func() {
				hostCanRunVMs = cloudhv.RequireKVM
				vmSocketDir = ensureIPCDir
			})

			entry := newVMRuntime(runtimeDeps{
				svc:    &session.Service{},
				cfg:    cfg,
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})

			if entry.name != tc.wantName {
				t.Errorf("name %q, want %q", entry.name, tc.wantName)
			}
			if entry.missing != tc.wantMissing {
				t.Errorf("missing %q, want %q", entry.missing, tc.wantMissing)
			}
			if (entry.driver != nil) != tc.wantDriver {
				t.Errorf("driver %v, want %v", entry.driver != nil, tc.wantDriver)
			}
			if entry.driver != nil {
				entry.cleanup()
			}
		})
	}
}

func writeStubFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("stub "+name), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
