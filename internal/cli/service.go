package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/service"
)

// cmdService is the router for `deepcove service ...`.
//
// Usage:
//
//	deepcove service install [--no-linger] [-- <flags for deepcove start>]
//	deepcove service uninstall
//	deepcove service status
//	deepcove service start | stop | restart
//
// Anything after `--` is what the installed service passes to `deepcove start`, so the service runs
// with the same flags you would have typed yourself.
func cmdService(ctx context.Context, args []string) error {
	if len(args) == 0 {
		serviceUsage(os.Stderr)
		return errors.New("service needs a verb (e.g. `deepcove service status`)")
	}

	switch args[0] {
	case "-h", "--help", "help":
		serviceUsage(os.Stdout)
		return nil
	}

	manager, err := newServiceManager(ctx)
	if err != nil {
		return err
	}

	switch args[0] {
	case "install":
		return cmdServiceInstall(manager, args[1:])
	case "uninstall":
		if err := manager.Uninstall(); err != nil {
			return err
		}
		fmt.Println("deepcove will no longer start on its own. Your logs and data are untouched.")
		return nil
	case "status":
		return printServiceStatus(manager)
	case "start":
		return manager.Start()
	case "stop":
		return manager.Stop()
	case "restart":
		return manager.Restart()
	}
	serviceUsage(os.Stderr)
	return fmt.Errorf("unknown service verb %q", args[0])
}

// newServiceManager resolves where this deepcove lives and where its data is, which is what the
// installed service will point at.
func newServiceManager(ctx context.Context) (service.Manager, error) {
	cfg, err := config.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("service: could not find this binary: %w", err)
	}
	return service.New(cfg.DataDir, exe)
}

func cmdServiceInstall(manager service.Manager, args []string) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	// Lingering is on by default: someone installing a background service means it to survive logout.
	noLinger := fs.Bool("no-linger", false, "on Linux, do not keep the service running after you log out")
	before, after := splitAtDashDash(args)
	if err := fs.Parse(before); err != nil {
		return err
	}

	if err := manager.Install(after, service.InstallOptions{Linger: !*noLinger}); err != nil {
		return err
	}
	fmt.Println("deepcove is installed. It starts now, and again with your session.")
	return printServiceStatus(manager)
}

// splitAtDashDash separates our own flags from the ones meant for `deepcove start`.
func splitAtDashDash(args []string) (before, after []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func printServiceStatus(manager service.Manager) error {
	status, err := manager.Status()
	if err != nil {
		return err
	}
	fmt.Printf("installed: %v\n", status.Installed)
	fmt.Printf("running:   %v\n", status.Running)
	fmt.Printf("defined:   %s\n", status.Unit)
	if len(status.StartCommand) > 0 {
		fmt.Printf("runs:      %s\n", strings.Join(status.StartCommand, " "))
	}
	if status.LogPath != "" {
		fmt.Printf("logs:      %s\n", status.LogPath)
	}
	if len(status.LogCommand) > 0 {
		fmt.Printf("read logs: %s\n", strings.Join(status.LogCommand, " "))
	}
	return nil
}

func serviceUsage(w *os.File) {
	lines := []string{
		"deepcove service: run deepcove in the background, starting with your session",
		"",
		"Usage:",
		"  deepcove service install [--no-linger] [-- <flags for deepcove start>]",
		"  deepcove service uninstall",
		"  deepcove service status",
		"  deepcove service start | stop | restart",
		"",
		"Example:",
		"  deepcove service install -- --cluster prod --addr 127.0.0.1:9090",
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}
