//go:build darwin

package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const launchdLabel = "dev.deepcove"

type launchdManager struct {
	plistPath string
	logPath   string
	exePath   string
	runner    commandRunner
}

// New builds the manager for this platform.
func New(dataDir, exePath string) (Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &launchdManager{
		plistPath: filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"),
		logPath:   filepath.Join(dataDir, "logs", "deepcove.log"),
		exePath:   exePath,
		runner:    execCommandRunner{},
	}, nil
}

// domain is the user's GUI session, which is where an agent belongs. A system domain would run without
// the home directory and could not start VMs.
func (m *launchdManager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func (m *launchdManager) target() string { return m.domain() + "/" + launchdLabel }

// plistText writes the agent.
//
// KeepAlive with SuccessfulExit false is launchd's way of saying "restart only on failure": a clean
// exit stays exited, a crash comes back. Nothing rotates StandardOutPath, which is the platform's
// answer rather than ours.
func plistText(command []string, logPath string) string {
	var args bytes.Buffer
	for _, arg := range command {
		args.WriteString("\n\t\t<string>")
		_ = xml.EscapeText(&args, []byte(arg))
		args.WriteString("</string>")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>%s
	</array>
	<!-- Stop deepcove, not what it started: without this launchd kills the whole process group, and a
	     session on the machine is a tmux server in it. -->
	<key>AbandonProcessGroup</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchdLabel, args.String(), logPath, logPath)
}

func (m *launchdManager) Install(extra []string, _ InstallOptions) error {
	if err := os.MkdirAll(filepath.Dir(m.plistPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.logPath), 0o755); err != nil {
		return err
	}
	text := plistText(startCommand(m.exePath, extra), m.logPath)
	if err := os.WriteFile(m.plistPath, []byte(text), 0o644); err != nil {
		return err
	}
	// An older copy may still be loaded, and bootstrap refuses in that case. Failing here is normal.
	_ = m.runner.run("launchctl", "bootout", m.target())
	return m.runner.run("launchctl", "bootstrap", m.domain(), m.plistPath)
}

func (m *launchdManager) Uninstall() error {
	_ = m.runner.run("launchctl", "bootout", m.target())
	if err := os.Remove(m.plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *launchdManager) Start() error {
	return m.runner.run("launchctl", "bootstrap", m.domain(), m.plistPath)
}

// Stop unloads rather than calling `launchctl stop`, which with KeepAlive set would only bounce it.
func (m *launchdManager) Stop() error { return m.runner.run("launchctl", "bootout", m.target()) }
func (m *launchdManager) Restart() error {
	return m.runner.run("launchctl", "kickstart", "-k", m.target())
}

func (m *launchdManager) Status() (Status, error) {
	status := Status{
		Unit:       m.plistPath,
		LogPath:    m.logPath,
		LogCommand: []string{"tail", "-f", m.logPath},
	}
	installed, err := os.ReadFile(m.plistPath)
	if err == nil {
		status.Installed = true
		status.StartCommand = commandFromPlist(string(installed))
	}
	out, err := m.runner.output("launchctl", "print", m.target())
	if err == nil {
		status.Running = strings.Contains(out, "state = running")
	}
	return status, nil
}

// commandFromPlist reads back what the installed agent runs, so status shows the flags it was actually
// installed with rather than the ones we would write today.
func commandFromPlist(text string) []string {
	var doc struct {
		Strings []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal([]byte(text), &doc); err != nil {
		return nil
	}
	return doc.Strings
}

// ProtectedFolders is where launchd reads this user's agents.
func ProtectedFolders(home string) []string {
	return []string{filepath.Join(home, "Library", "LaunchAgents")}
}
