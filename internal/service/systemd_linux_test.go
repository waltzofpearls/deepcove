//go:build linux

package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) (*systemdManager, *fakeRunner) {
	t.Helper()
	runner := newFakeRunner()
	return &systemdManager{
		unitPath: filepath.Join(t.TempDir(), "systemd", "user", unitName),
		exePath:  "/usr/local/bin/deepcove",
		runner:   runner,
	}, runner
}

func TestUnitText(t *testing.T) {
	text := unitText([]string{"/usr/local/bin/deepcove", "start", "--addr", "127.0.0.1:9090"})

	if !strings.Contains(text, "ExecStart=/usr/local/bin/deepcove start --addr 127.0.0.1:9090") {
		t.Errorf("unit runs the wrong thing:\n%s", text)
	}
	// on-failure, not always: a deliberate exit has to stay exited.
	if !strings.Contains(text, "Restart=on-failure") {
		t.Errorf("unit would restart after a clean exit:\n%s", text)
	}
	// journald owns the output. Redirecting to a file would take rotation away from the platform.
	if strings.Contains(text, "StandardOutput") {
		t.Errorf("unit redirects output away from the journal:\n%s", text)
	}
	if !strings.Contains(text, "WantedBy=default.target") {
		t.Errorf("unit will not start with the session:\n%s", text)
	}
	// Without this systemd kills every process in the unit's group on stop, taking every session's tmux
	// server with it, and nothing is left to take back at the next start.
	if !strings.Contains(text, "KillMode=process") {
		t.Errorf("stopping deepcove would stop the sessions too:\n%s", text)
	}
}

func TestInstallWritesTheUnitThenEnablesIt(t *testing.T) {
	manager, runner := newTestManager(t)
	if err := manager.Install([]string{"--addr", "127.0.0.1:9090"}, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}

	written, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if !strings.Contains(string(written), "--addr 127.0.0.1:9090") {
		t.Error("the flags asked for at install time are missing from the unit")
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + unitName,
	}
	if !slices.Equal(runner.calls, want) {
		t.Errorf("calls %v, want %v", runner.calls, want)
	}
}

// Lingering is a system level setting, so failing to enable it must not fail the install.
func TestInstallSurvivesLingerBeingRefused(t *testing.T) {
	manager, runner := newTestManager(t)
	runner.fail["loginctl"] = true

	if err := manager.Install(nil, InstallOptions{Linger: true}); err != nil {
		t.Fatalf("install failed because lingering did: %v", err)
	}
	var tried bool
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "loginctl enable-linger") {
			tried = true
		}
	}
	if !tried {
		t.Error("lingering was asked for and never attempted")
	}
}

func TestUninstallDisablesThenRemoves(t *testing.T) {
	manager, runner := newTestManager(t)
	if err := manager.Install(nil, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	runner.calls = nil

	if err := manager.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(manager.unitPath); !os.IsNotExist(err) {
		t.Error("the unit file is still there")
	}
	want := []string{
		"systemctl --user disable --now " + unitName,
		"systemctl --user daemon-reload",
	}
	if !slices.Equal(runner.calls, want) {
		t.Errorf("calls %v, want %v", runner.calls, want)
	}
}

func TestStatusReportsWhatIsInstalledAndRunning(t *testing.T) {
	manager, runner := newTestManager(t)
	if err := manager.Install([]string{"--addr", "127.0.0.1:9090"}, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	runner.stdout["systemctl"] = "active\n"

	status, err := manager.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Running {
		t.Errorf("installed=%v running=%v, want both true", status.Installed, status.Running)
	}
	want := []string{"/usr/local/bin/deepcove", "start", "--addr", "127.0.0.1:9090"}
	if !slices.Equal(status.StartCommand, want) {
		t.Errorf("start command %v, want %v", status.StartCommand, want)
	}
	// journald keeps the output, so there is no file of ours to name.
	if status.LogPath != "" {
		t.Errorf("log path %q, want none", status.LogPath)
	}
	if !slices.Equal(status.LogCommand, []string{"journalctl", "--user", "-u", unitName, "-f"}) {
		t.Errorf("log command %v", status.LogCommand)
	}
}

func TestStatusOnAMachineWithNothingInstalled(t *testing.T) {
	manager, _ := newTestManager(t)
	status, err := manager.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Installed || status.StartCommand != nil {
		t.Errorf("reported something installed: %+v", status)
	}
}
