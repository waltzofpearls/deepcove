//go:build linux

package service

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const unitName = "deepcove.service"

type systemdManager struct {
	unitPath string
	exePath  string
	runner   commandRunner
}

// New builds the manager for this platform. The data directory is unused here: journald keeps the
// output, so there is no log file of ours to place.
func New(_, exePath string) (Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &systemdManager{
		unitPath: filepath.Join(home, ".config", "systemd", "user", unitName),
		exePath:  exePath,
		runner:   execCommandRunner{},
	}, nil
}

// unitText writes the user unit.
//
// No StandardOutput: output goes to the journal, which rotates and caps it. Restart=on-failure rather
// than always, so a deliberate exit stays exited, which is the choice syncthing ships.
func unitText(command []string) string {
	return fmt.Sprintf(`[Unit]
Description=deepcove
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=1

[Install]
WantedBy=default.target
`, strings.Join(command, " "))
}

func (m *systemdManager) Install(extra []string, opts InstallOptions) error {
	if err := os.MkdirAll(filepath.Dir(m.unitPath), 0o755); err != nil {
		return err
	}
	text := unitText(startCommand(m.exePath, extra))
	if err := os.WriteFile(m.unitPath, []byte(text), 0o644); err != nil {
		return err
	}
	if err := m.runner.run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if opts.Linger {
		m.enableLinger()
	}
	return m.runner.run("systemctl", "--user", "enable", "--now", unitName)
}

// enableLinger is best effort on purpose. It is a system level setting, usually needing root or a
// polkit prompt, and it changes how every user service behaves after logout, so a failure prints the
// command rather than failing the install.
func (m *systemdManager) enableLinger() {
	name := ""
	if current, err := user.Current(); err == nil {
		name = current.Username
	}
	if err := m.runner.run("loginctl", "enable-linger", name); err != nil {
		fmt.Printf("could not enable lingering, so the service stops when you log out.\n"+
			"run this yourself to keep it running:\n  sudo loginctl enable-linger %s\n", name)
	}
}

func (m *systemdManager) Uninstall() error {
	_ = m.runner.run("systemctl", "--user", "disable", "--now", unitName)
	if err := os.Remove(m.unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.runner.run("systemctl", "--user", "daemon-reload")
}

func (m *systemdManager) Start() error { return m.runner.run("systemctl", "--user", "start", unitName) }
func (m *systemdManager) Stop() error  { return m.runner.run("systemctl", "--user", "stop", unitName) }
func (m *systemdManager) Restart() error {
	return m.runner.run("systemctl", "--user", "restart", unitName)
}

func (m *systemdManager) Status() (Status, error) {
	status := Status{
		Unit:       m.unitPath,
		LogCommand: []string{"journalctl", "--user", "-u", unitName, "-f"},
	}
	installed, err := os.ReadFile(m.unitPath)
	if err == nil {
		status.Installed = true
		status.StartCommand = commandFromUnit(string(installed))
	}
	// `systemctl status` is prose and exits non-zero when inactive. `show` is the machine interface.
	out, _ := m.runner.output("systemctl", "--user", "show", "-p", "ActiveState", "--value", unitName)
	status.Running = strings.TrimSpace(out) == "active"
	return status, nil
}

// commandFromUnit reads back what the installed unit runs, so status shows the flags it was actually
// installed with rather than the ones we would write today.
func commandFromUnit(text string) []string {
	for line := range strings.SplitSeq(text, "\n") {
		if after, ok := strings.CutPrefix(line, "ExecStart="); ok {
			return strings.Fields(after)
		}
	}
	return nil
}
