package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/service"
	"github.com/waltzofpearls/deepcove/internal/session"
)

// cmdService is the router for `deepcove service ...`.
//
// Usage:
//
//	deepcove service install [--no-linger] [--env KEY=VALUE] [-- <flags for deepcove start>]
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
		return cmdServiceInstall(ctx, manager, args[1:])
	case "uninstall":
		stopSessionsBeforeUninstall(ctx)
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
		// Counted first: once the service is down, nothing is left to ask what it was running.
		agents := agentsStillRunning(ctx)
		if err := manager.Stop(); err != nil {
			return err
		}
		fmt.Println(serviceStoppedMessage(agents))
		return nil
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

// uninstallStopBudget bounds the stopping. A session that will not go must not leave you unable to
// uninstall, and the service is stopped right after this anyway.
const uninstallStopBudget = 30 * time.Second

// stopSessionsBeforeUninstall stops what is running while deepcove is still there to ask. A session on
// this machine is a tmux server that outlives deepcove, so uninstalling around one would leave an agent
// running with nothing left to reach it.
func stopSessionsBeforeUninstall(ctx context.Context) {
	cfg, err := config.Load(ctx)
	if err != nil {
		fmt.Printf("could not read the config, so nothing was stopped: %v\n", err)
		return
	}
	stopCtx, cancel := context.WithTimeout(ctx, uninstallStopBudget)
	defer cancel()
	stopped, err := stopRunningSessions(stopCtx, newAPIClient(cfg))
	if stopped > 0 {
		fmt.Printf("stopped %d %s.\n", stopped, plural(stopped, "session", "sessions"))
	}
	if err != nil {
		fmt.Printf("could not stop every session: %v\n", err)
		fmt.Println("an agent left running keeps going. Start deepcove, stop it there, then uninstall again.")
	}
}

// agentsStillRunning is how many sessions are left behind by a deepcove that stops: the ones whose
// sandboxes outlive it. Zero when deepcove cannot be asked, since the line this feeds is worth nothing
// if it is a guess.
func agentsStillRunning(ctx context.Context) int {
	cfg, err := config.Load(ctx)
	if err != nil {
		return 0
	}
	sessions, err := runningSessions(ctx, newAPIClient(cfg))
	if err != nil {
		return 0
	}
	return len(slices.DeleteFunc(sessions, func(sess *session.Session) bool {
		return !session.SandboxesOutliveDeepcove(sess.Runtime)
	}))
}

func serviceStoppedMessage(agents int) string {
	if agents == 0 {
		return "deepcove is stopped."
	}
	return fmt.Sprintf("deepcove is stopped. %d %s still running on this machine, "+
		"and starting deepcove again takes them back.", agents, plural(agents, "agent is", "agents are"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func cmdServiceInstall(ctx context.Context, manager service.Manager, args []string) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	// Lingering is on by default: someone installing a background service means it to survive logout.
	noLinger := fs.Bool("no-linger", false, "on Linux, do not keep the service running after you log out")
	var env envFlag
	fs.Var(&env, "env", "a KEY=VALUE the service should start with, repeatable")
	before, after := splitAtDashDash(args)
	if err := fs.Parse(before); err != nil {
		return err
	}

	// Into the data dir, not the service definition: a unit file is world readable and permanent, and
	// one of these is usually a token.
	if len(env) > 0 {
		cfg, err := config.Load(ctx)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if err := config.SetEnvFileValues(cfg.DataDir, env); err != nil {
			return err
		}
		fmt.Printf("stored %d setting(s) in %s\n", len(env), config.EnvFilePath(cfg.DataDir))
	}

	if err := manager.Install(after, service.InstallOptions{Linger: !*noLinger}); err != nil {
		return err
	}
	fmt.Println("deepcove is running in the background, and starts on its own from now on.")
	return printServiceStatus(manager)
}

// envFlag collects KEY=VALUE settings. Unlike repeatedFlag it never splits on commas, since a value may
// contain one.
type envFlag []string

func (e *envFlag) String() string {
	return strings.Join(*e, " ")
}

func (e *envFlag) Set(value string) error {
	*e = append(*e, value)
	return nil
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
		"  deepcove service install [--no-linger] [--env KEY=VALUE] [-- <flags for deepcove start>]",
		"  deepcove service uninstall",
		"  deepcove service status",
		"  deepcove service start | stop | restart",
		"",
		"Example:",
		"  deepcove service install -- --addr 127.0.0.1:9090",
	}
	_, _ = fmt.Fprint(w, joinStringLines(lines))
}
