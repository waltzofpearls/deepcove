package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/ghrelease"
	"github.com/waltzofpearls/deepcove/internal/upgrade"
)

// cmdUpgrade replaces this binary with a release from GitHub.
//
// Usage:
//
//	deepcove upgrade [--check] [--version v0.2.0]
//
// The archive is checked against the release's SHA256SUMS before anything on disk is touched. If
// deepcove is installed as a service and running, it is restarted at the end.
func cmdUpgrade(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	check := fs.Bool("check", false, "report what is available and change nothing")
	wanted := fs.String("version", "", "install this exact tag instead of the newest, e.g. v0.2.0")
	if err := fs.Parse(args); err != nil {
		return err
	}

	exe, err := currentBinary()
	if err != nil {
		return err
	}

	client := ghrelease.New()
	release, err := findRelease(ctx, client, *wanted)
	if err != nil {
		return err
	}
	fmt.Printf("installed: %s\n", Version)
	fmt.Printf("available: %s\n", release.Tag)
	if release.Tag == Version {
		fmt.Println("already up to date")
		return nil
	}

	// Homebrew tracks the copy it installed. Replacing that file leaves brew believing it still has the
	// old version, so send people to brew instead, which is what gh does.
	if formula, ok := homebrewFormula(exe); ok {
		fmt.Printf("%s came from Homebrew\n", exe)
		if formula == "" {
			fmt.Println("to upgrade, use brew rather than this command")
			return nil
		}
		fmt.Printf("to upgrade, run: brew upgrade %s\n", formula)
		return nil
	}
	if *check {
		fmt.Printf("to upgrade, run: %s upgrade\n", exe)
		return nil
	}

	dir := filepath.Dir(exe)
	if !upgrade.CanWrite(dir) {
		return fmt.Errorf("%s is not yours to write, run: sudo %s upgrade", dir, exe)
	}

	name := upgrade.AssetName(release.Tag, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("downloading %s...\n", name)
	progress := newDownloadProgress()
	archive, err := client.Download(ctx, release, name, dir, progress.report)
	progress.finish()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()

	binary, err := ghrelease.ExtractBinary(archive, upgrade.BinaryName(runtime.GOOS), dir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(binary) }()

	if err := upgrade.Replace(exe, binary); err != nil {
		return err
	}
	fmt.Printf("upgraded %s to %s\n", exe, release.Tag)
	restartServiceIfRunning(ctx, exe)
	return nil
}

func findRelease(ctx context.Context, client *ghrelease.Client, tag string) (ghrelease.Release, error) {
	if tag == "" {
		return client.Latest(ctx)
	}
	return client.ByTag(ctx, tag)
}

// currentBinary is the file to replace, with symlinks resolved: what is in PATH is often a link, and
// the real file behind it is the one that has to change.
func currentBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("upgrade: could not find this binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}

// homebrewFormula reports whether this binary lives under Homebrew's prefix, and which formula put it
// there. The name is empty when the binary is under the prefix but outside the Cellar.
func homebrewFormula(exe string) (string, bool) {
	brew, err := exec.LookPath("brew")
	if err != nil {
		return "", false
	}
	out, err := exec.Command(brew, "--prefix").Output()
	if err != nil {
		return "", false
	}
	prefix := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	if !underPrefix(exe, prefix) {
		return "", false
	}
	return cellarFormula(exe, prefix), true
}

// cellarFormula reads the formula out of a Cellar path, which looks like
// <prefix>/Cellar/<formula>/<version>/bin/deepcove.
func cellarFormula(exe, prefix string) string {
	rel, err := filepath.Rel(filepath.Join(prefix, "Cellar"), exe)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 || parts[0] == "." || parts[0] == ".." {
		return ""
	}
	return parts[0]
}

func underPrefix(exe, prefix string) bool {
	if prefix == "" {
		return false
	}
	rel, err := filepath.Rel(prefix, exe)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// restartServiceIfRunning picks up the new binary. A service that is not installed, or is stopped, is
// left alone, and a restart that fails does not undo an upgrade that worked.
func restartServiceIfRunning(ctx context.Context, exe string) {
	manager, err := newServiceManager(ctx)
	if err != nil {
		return
	}
	status, err := manager.Status()
	if err != nil || !status.Installed || !status.Running {
		return
	}
	if err := manager.Restart(); err != nil {
		fmt.Printf("could not restart the service (%v), run: %s service restart\n", err, exe)
		return
	}
	fmt.Println("restarted the deepcove service on the new version")
}
