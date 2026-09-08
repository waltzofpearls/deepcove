package cloudhv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AppArmorProfileName is the profile that lets passt build the namespaces it isolates itself with.
const AppArmorProfileName = "deepcove-passt"

// InstalledAppArmorProfile is where a host keeps the profile, and where everything on boot loads it from.
const InstalledAppArmorProfile = "/etc/apparmor.d/" + AppArmorProfileName

// restrictionPath is Ubuntu's switch for unprivileged namespaces, which is what passt needs to isolate
// itself.
const restrictionPath = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// hostSandboxFacts are what this host says about letting passt isolate itself. A value rather than three
// calls, so the decision it feeds can be tested without the host.
type hostSandboxFacts struct {
	namespacesRestricted bool
	profileInstalled     bool
	// root is whether deepcove can hand passt root, which is the only fallback left without the profile.
	root bool
}

func hostSandbox() hostSandboxFacts {
	return hostSandboxFacts{
		namespacesRestricted: fileSaysOne(restrictionPath),
		profileInstalled:     fileExists(InstalledAppArmorProfile),
		root:                 os.Geteuid() == 0,
	}
}

// needsProfile reports that passt cannot build its namespaces here until the profile is installed.
func (f hostSandboxFacts) needsProfile() bool {
	return f.namespacesRestricted && !f.profileInstalled
}

// passtRunAsArgs keeps passt root where dropping privileges would leave it unable to make its own
// namespaces. Nothing otherwise: a root passt drops to nobody by itself, and a passt that is not root
// cannot change user at all, so asking it to is an error.
func passtRunAsArgs() []string {
	return passtRunAsArgsFor(hostSandbox())
}

func passtRunAsArgsFor(facts hostSandboxFacts) []string {
	if facts.needsProfile() && facts.root {
		return []string{"--runas", "0:0"}
	}
	return nil
}

// PasstSandboxNeedsAppArmor reports whether this host stops passt isolating itself. Ubuntu 24.04 does,
// until the profile is loaded.
func PasstSandboxNeedsAppArmor() bool {
	return hostSandbox().needsProfile()
}

func fileSaysOne(path string) bool {
	text, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(text)) == "1"
}

// fileExists asks the filesystem, not the kernel: the kernel's list of loaded profiles is closed to
// anyone but root, and deepcove usually is not root. A profile sitting in /etc/apparmor.d is loaded on
// every boot, so its presence is the answer a normal user can actually get.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// apparmorParserTimeout bounds loading a profile into the kernel.
const apparmorParserTimeout = 10 * time.Second

// loadProfile puts a profile into the kernel. A variable so a test can watch it without an AppArmor host.
var loadProfile = runApparmorParser

// InstallAppArmorProfile writes the profile this host needs and loads it, which only root can do. Better
// than working around the missing profile, because the workaround is passt keeping root for good.
func InstallAppArmorProfile(passtBinary string) error {
	return installAppArmorProfile(InstalledAppArmorProfile, passtBinary)
}

func installAppArmorProfile(path, passtBinary string) error {
	if err := os.WriteFile(path, []byte(AppArmorProfile(passtBinary)), 0o644); err != nil {
		return fmt.Errorf("cloudhv: write %s: %w", path, err)
	}
	return loadProfile(path)
}

func runApparmorParser(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), apparmorParserTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "apparmor_parser", "-r", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cloudhv: load %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AppArmorProfile is the profile this host needs so passt can isolate itself. The path is a glob over
// the passt versions a node may hold, so an upgrade does not need a new profile.
func AppArmorProfile(passtBinary string) string {
	versions := filepath.Dir(filepath.Dir(passtBinary))
	pattern := filepath.Join(versions, "*", filepath.Base(passtBinary))
	return fmt.Sprintf(`abi <abi/4.0>,
include <tunables/global>

profile %s %s flags=(unconfined) {
  userns,
  include if exists <local/%s>
}
`, AppArmorProfileName, pattern, AppArmorProfileName)
}
