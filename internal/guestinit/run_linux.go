//go:build linux

package guestinit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/guestctl"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
)

// tmuxSession is the session name the HOST's entrypoint creates. It matches the Docker runtime so
// session/attach.go and session/preview.go, which target "main", work identically over the VF runtime.
// The guest no longer creates the session, so this exists only to name it accurately in the boot log.
const tmuxSession = "main"

// Run is the guest PID 1. It brings up the pseudo filesystems, overlays a writable tmpfs on the read
// only root, pivots into the merged root, launches the agent under tmux, then reaps orphans forever as
// PID 1 must. It follows the standard Firecracker overlay-init pattern; the full sequence can only be
// validated inside a real guest boot, which is wired up during driver assembly.
// Run never returns while the VM is alive, and never exits on a recoverable failure. PID 1 exiting makes
// the kernel panic with "Attempted to kill init", which buries the real cause under a stack trace, so any
// failure instead logs loudly and drops to a console shell that keeps the guest inspectable.
func Run() error {
	logf("deepcove guest init starting")
	cfg, err := bringUp()
	if err != nil {
		logf("ERROR: %v", err)
		logf("dropping to a console shell; the guest stays up so you can investigate")
		holdConsole()
		return nil
	}
	logf("session entrypoint started; agent will appear in tmux session %q", tmuxSession)
	serveExec()
	serveControl(cfg)
	reapForever()
	return nil
}

// serveExec starts the vsock exec service the host dials to run commands and attach terminals. A failure
// here is logged rather than fatal: the agent is already running, so losing exec is a degraded session
// rather than a dead one.
func serveExec() {
	go func() {
		if err := guestexec.ListenAndServe(context.Background()); err != nil {
			logf("WARNING: exec service stopped: %v", err)
		}
	}()
	logf("exec service listening on vsock port %d", guestexec.Port)
}

// serveControl starts the vsock control service, which is how the host asks the guest to shut down. The
// Kata kernel has no gpio-keys driver, so Apple VF's RequestStop never reaches the guest and this is the
// only clean way to halt it.
func serveControl(cfg Config) {
	handler := guestctl.Handler{
		Shutdown: func() error {
			logf("shutdown requested by the host")
			// Hand back whatever the agent refreshed before the machine goes away. A failure here must
			// not block the shutdown, or a broken broker would leave VMs running.
			if err := returnCredentials(cfg); err != nil {
				logf("WARNING: could not return credentials: %v", err)
			}
			return poweroff()
		},
	}
	go func() {
		if err := guestctl.ListenAndServe(context.Background(), handler); err != nil {
			logf("WARNING: control service stopped: %v", err)
		}
	}()
	logf("control service listening on vsock port %d", guestctl.Port)
}

// bringUp is the boot sequence proper: pseudo filesystems, the overlay root, the pivot, then the agent.
func bringUp() (Config, error) {
	// Set init's own environment first. exec.Command resolves a bare binary name against the CURRENT
	// process's PATH (not cmd.Env), so this has to happen before anything is launched, and children then
	// inherit it for free.
	for k, v := range defaultEnv() {
		if err := os.Setenv(k, v); err != nil {
			return Config{}, fmt.Errorf("set %s: %w", k, err)
		}
	}
	if err := mountPseudo(); err != nil {
		return Config{}, err
	}
	logf("pseudo filesystems mounted")

	// Fetch the session's configuration before building the overlay, because it carries the overlay's
	// size cap. vsock only needs /dev, which is mounted above, so this works well before networking.
	hostCfg, err := fetchConfig()
	if err != nil {
		return Config{}, fmt.Errorf("fetch session config: %w", err)
	}
	// The single place the guest's own config is resolved. It used to be built by main from files baked
	// into the image and then mutated HERE, on a by-value copy, so the caller kept the stale one: that is
	// why the shutdown credential handback saw an empty agent and quietly did nothing.
	cfg := Config{Agent: hostCfg.Agent}

	if err := setupOverlayRoot(hostCfg.OverlayBytes); err != nil {
		return Config{}, err
	}
	logf("overlay root assembled (writable layer cap: %s)", overlayCapLabel(hostCfg.OverlayBytes))
	if err := pivotInto(newRoot); err != nil {
		return Config{}, err
	}
	// Re-mount the pseudo filesystems inside the new root; the pivot left them behind on the old root.
	if err := mountPseudo(); err != nil {
		return Config{}, err
	}
	logf("pivoted into overlay root")

	// Networking and credentials are both best effort: a session without either is degraded but still
	// usable, and failing the boot over them would cost the console and exec needed to diagnose it.
	if err := setupNetwork(); err != nil {
		logf("WARNING: network setup failed, the session has no network: %v", err)
	} else {
		logf("network up")
	}
	if err := fetchCredentials(cfg); err != nil {
		logf("WARNING: could not restore credentials: %v", err)
	}
	// Keep the host in step with logins that happen inside this VM, so a session created later starts
	// already authenticated instead of prompting again.
	go watchCredentials(cfg)

	return cfg, runEntrypoint(hostCfg)
}

// runEntrypoint runs the session's entrypoint, the same script the Docker runtime uses: it performs the
// agent setup (installing tmux and the agent when the image lacks them) and then starts the agent in a
// tmux session named "main". Running the identical script is what keeps attach and preview working the
// same on both runtimes.
func runEntrypoint(hostCfg guestcfg.Config) error {
	argv := hostCfg.Entrypoint
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv comes from the trusted host side
	cmd.Env = append(os.Environ(), envList(hostCfg.Env)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// The entrypoint ends in `sleep infinity` to hold the container open, which a VM does not need:
	// init stays alive on its own. Run it in the background and let init get on with serving vsock.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start entrypoint: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// holdConsole hands the console to an interactive shell so a broken image can be debugged from the VM
// itself. If no shell can be started it still keeps init alive, because exiting would panic the kernel.
func holdConsole() {
	for _, sh := range shellCandidates() {
		cmd := exec.Command(sh) //nolint:gosec // fixed list of shell paths, no user input
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err == nil {
			logf("console shell exited; holding the guest open")
			break
		}
	}
	reapForever()
}

// logf writes a progress line to the console (stdout is the virtio serial port), so a real boot is
// observable and a failing step is obvious.
func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stdout, "[deepcove-init] "+format+"\n", args...)
}

func mountPseudo() error {
	for _, m := range pseudoMounts() {
		if err := os.MkdirAll(m.Target, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", m.Target, err)
		}
		if err := unix.Mount(m.Source, m.Target, m.FSType, m.Flags, m.Data); err != nil && !mountAlreadyDone(err) {
			return fmt.Errorf("mount %s on %s: %w", m.FSType, m.Target, err)
		}
	}
	return nil
}

// setupOverlayRoot mounts a tmpfs over the baked OverlayMountDir, creates the overlay directories inside
// it, and assembles the merged root at newRoot with the read only root as the lower layer.
//
// OverlayMountDir must already exist in the image: the base is mounted read only, so init cannot create
// any directory until it has a writable filesystem, and the tmpfs is what provides one. The tmpfs upper
// keeps the session throwaway and needs no mkfs in the guest; a persistent scratch block device is a
// later refinement.
func setupOverlayRoot(sizeBytes int64) error {
	if err := unix.Mount("tmpfs", OverlayMountDir, "tmpfs", 0, tmpfsSizeOption(sizeBytes)); err != nil {
		return fmt.Errorf("mount tmpfs on %s (is it baked into the image?): %w", OverlayMountDir, err)
	}
	for _, d := range []string{upperDir, workDir, newRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	if err := unix.Mount("overlay", newRoot, "overlay", 0, defaultOverlay().mountData()); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}
	return nil
}

// pivotInto makes root the new root and detaches the old one, the standard pivot_root dance. pivot_root
// requires root to be a mount point, which the overlay mount already satisfies, so no self bind is
// needed. oldRoot lives on the overlay and so is writable.
func pivotInto(root string) error {
	oldRoot := root + "/oldroot"
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir oldroot: %w", err)
	}
	if err := unix.PivotRoot(root, oldRoot); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir new root: %w", err)
	}
	if err := unix.Unmount("/oldroot", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach oldroot: %w", err)
	}
	return os.Remove("/oldroot")
}

// reapForever is the PID 1 duty: adopt and reap orphaned children so the guest never accumulates
// zombies. It blocks for the life of the VM.
func reapForever() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, unix.SIGCHLD)
	for range sigs {
		for {
			var ws unix.WaitStatus
			pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}
