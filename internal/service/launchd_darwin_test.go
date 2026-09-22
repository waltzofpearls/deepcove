//go:build darwin

package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) (*launchdManager, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	runner := newFakeRunner()
	return &launchdManager{
		plistPath: filepath.Join(dir, "LaunchAgents", launchdLabel+".plist"),
		logPath:   filepath.Join(dir, "logs", "deepcove.log"),
		exePath:   "/usr/local/bin/deepcove",
		runner:    runner,
	}, runner
}

func TestPlistText(t *testing.T) {
	text := plistText([]string{"/usr/local/bin/deepcove", "start", "--addr", "127.0.0.1:9090"}, "/tmp/deepcove.log")

	for _, want := range []string{
		"<string>dev.deepcove</string>",
		"<string>/usr/local/bin/deepcove</string>",
		"<string>--addr</string>",
		"<key>StandardOutPath</key>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist is missing %q:\n%s", want, text)
		}
	}
	// KeepAlive with SuccessfulExit false is what makes it restart on a crash but stay down after a
	// clean exit. Losing this line would turn stop into a restart loop.
	if !strings.Contains(text, "<key>SuccessfulExit</key>\n\t\t<false/>") {
		t.Errorf("plist does not restart on failure only:\n%s", text)
	}
	// Without this launchd kills the process group on stop, taking every session's tmux server with it,
	// and nothing is left to take back at the next start.
	if !strings.Contains(text, "<key>AbandonProcessGroup</key>\n\t<true/>") {
		t.Errorf("stopping deepcove would stop the sessions too:\n%s", text)
	}
}

// A path with an ampersand or a quote must not break the file it is written into.
func TestPlistTextEscapesArguments(t *testing.T) {
	text := plistText([]string{"/opt/a&b/deepcove", "start", "--addr", "<script>"}, "/tmp/log")
	if strings.Contains(text, "a&b") || strings.Contains(text, "<script>") {
		t.Errorf("argument was written raw into the plist:\n%s", text)
	}
	if !strings.Contains(text, "a&amp;b") || !strings.Contains(text, "&lt;script&gt;") {
		t.Errorf("argument was not escaped:\n%s", text)
	}
}

func TestInstallWritesThePlistThenLoadsIt(t *testing.T) {
	manager, runner := newTestManager(t)
	if err := manager.Install([]string{"--addr", "127.0.0.1:9090"}, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}

	written, err := os.ReadFile(manager.plistPath)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if !strings.Contains(string(written), "--addr") {
		t.Error("the flags asked for at install time are missing from the plist")
	}
	// The old copy has to be unloaded first, or bootstrap refuses.
	want := []string{
		"launchctl bootout " + manager.target(),
		"launchctl bootstrap " + manager.domain() + " " + manager.plistPath,
	}
	if !slices.Equal(runner.calls, want) {
		t.Errorf("calls %v, want %v", runner.calls, want)
	}
}

func TestUninstallUnloadsThenRemoves(t *testing.T) {
	manager, runner := newTestManager(t)
	if err := manager.Install(nil, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	runner.calls = nil

	if err := manager.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(manager.plistPath); !os.IsNotExist(err) {
		t.Error("the plist is still there")
	}
	if len(runner.calls) != 1 || !strings.HasPrefix(runner.calls[0], "launchctl bootout") {
		t.Errorf("calls %v, want a single bootout", runner.calls)
	}
}

func TestStatusReportsWhatIsInstalledAndRunning(t *testing.T) {
	manager, runner := newTestManager(t)
	if err := manager.Install([]string{"--addr", "127.0.0.1:9090"}, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	runner.stdout["launchctl"] = "state = running\npid = 4242\n"

	status, err := manager.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Running {
		t.Errorf("installed=%v running=%v, want both true", status.Installed, status.Running)
	}
	// Read back from the file, so status shows the flags it was installed with.
	want := []string{"/usr/local/bin/deepcove", "start", "--addr", "127.0.0.1:9090"}
	if !slices.Equal(status.StartCommand, want) {
		t.Errorf("start command %v, want %v", status.StartCommand, want)
	}
	if status.LogPath != manager.logPath {
		t.Errorf("log path %q, want %q", status.LogPath, manager.logPath)
	}
	if !slices.Equal(status.LogCommand, []string{"tail", "-f", manager.logPath}) {
		t.Errorf("log command %v", status.LogCommand)
	}
}

func TestStatusOnAMachineWithNothingInstalled(t *testing.T) {
	manager, _ := newTestManager(t)
	status, err := manager.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Installed || status.Running || status.StartCommand != nil {
		t.Errorf("reported something installed: %+v", status)
	}
}
