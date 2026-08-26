// Package service installs deepcove as a background service that starts with your session and comes
// back if it dies.
//
// It is always a per-user service, never a system one: deepcove needs your home directory and your
// credentials, and on macOS it has to run inside your login session to start VMs at all. Each platform
// has its own file in this package.
package service

import (
	"fmt"
	"os/exec"
	"strings"
)

// Status is what `deepcove service status` reports.
type Status struct {
	Installed bool
	Running   bool
	Unit      string // where this platform's service definition lives

	// LogPath is the file this platform writes output to, empty when the platform keeps the logs
	// itself, as journald does.
	LogPath string
	// LogCommand shows that output, whether it comes from a file or from the platform's log store.
	LogCommand []string
	// StartCommand is what the service runs when it starts.
	StartCommand []string
}

// InstallOptions carries choices that only matter on one platform, so the CLI stays platform free.
type InstallOptions struct {
	// Linger keeps a Linux user service running after logout and starts it at boot. Ignored on macOS,
	// where a login agent already behaves that way.
	Linger bool
}

// Manager drives one platform's service tooling.
type Manager interface {
	Install(extra []string, opts InstallOptions) error
	Uninstall() error
	Status() (Status, error)
	Start() error
	Stop() error
	Restart() error
}

// startCommand is what the service runs: this binary, then start, then whatever was asked for at
// install time. Absolute, because a service has no PATH worth trusting.
func startCommand(exePath string, extra []string) []string {
	return append([]string{exePath, "start"}, extra...)
}

// commandRunner runs the platform's own tool. An interface so install and uninstall can be tested
// without touching launchd or systemd.
type commandRunner interface {
	run(name string, args ...string) error
	output(name string, args ...string) (string, error)
}

type execCommandRunner struct{}

func (execCommandRunner) run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (execCommandRunner) output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
