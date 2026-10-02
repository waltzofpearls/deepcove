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
# Stop deepcove, not what it started. A session on the machine is a tmux server outside deepcove, and
# the default kills every process in the unit's group, agents included.
KillMode=process
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
	if opts.Linger {
		// Before systemctl, not after: lingering starts this user's service manager, and without one
		// there is no bus for systemctl --user to talk to.
		m.enableLinger()
	}
	if err := m.runner.run("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("%w\n%s", err, noLoginSessionHint)
	}
	return m.runner.run("systemctl", "--user", "enable", "--now", unitName)
}

// noLoginSessionHint explains the one failure people actually hit: `su - user` has no user bus.
const noLoginSessionHint = "a user service needs a login session. Log in as this user over ssh, " +
	"or run: sudo loginctl enable-linger <user>"

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

// ProtectedFolders are where the user's systemd reads units, drop-ins and environment. Defaults always
// count: systemd's XDG settings may differ from deepcove's.
func ProtectedFolders(home string) []string {
	folders := []string{
		filepath.Join(home, ".config", "systemd"),
		filepath.Join(home, ".config", "environment.d"),
		filepath.Join(home, ".local", "share", "systemd"),
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		folders = append(folders, filepath.Join(dir, "systemd"), filepath.Join(dir, "environment.d"))
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		folders = append(folders, filepath.Join(dir, "systemd"))
	}
	return folders
}
