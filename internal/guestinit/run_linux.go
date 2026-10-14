//go:build linux

package guestinit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/guestctl"
	"github.com/waltzofpearls/deepcove/internal/guestexec"
	"github.com/waltzofpearls/deepcove/internal/netguard"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Run is the guest PID 1. It brings up the pseudo filesystems, overlays a writable tmpfs on the read
// only root, pivots into the merged root, runs the session's setup, then reaps orphans forever as PID 1
// must. The full sequence can only be validated inside a real guest boot.
// Run never returns while the VM is alive, and never exits on a recoverable failure. PID 1 exiting makes
// the kernel panic with "Attempted to kill init", which buries the real cause under a stack trace, so any
// failure instead logs loudly and drops to a console shell that keeps the guest inspectable.
func Run() error {
	logf("deepcove guest init starting")
	// Built before anything is launched: it is both the record of this init's children and the only
	// thing allowed to wait on them, so nothing may start a process before it exists.
	children := newProcessTable()
	if err := bringUp(children); err != nil {
		logf("ERROR: %v", err)
		logf("dropping to a console shell; the guest stays up so you can investigate")
		holdConsole(children)
		return nil
	}
	logf("session entrypoint started; the host starts the agent once setup finishes")
	serveExec(children)
	serveControl()
	go followHardwareClock()
	children.reapForever()
	return nil
}

// serviceRestartDelay is how long to wait before standing a vsock service back up.
const serviceRestartDelay = time.Second

// serveExec starts the vsock exec service the host dials to run commands and attach terminals, and to
// start the agent in the first place. A failure here is logged rather than fatal, and the service keeps
// being stood back up, since without it the host can neither start the agent nor attach.
func serveExec(children *processTable) {
	go func() {
		// Stood back up when it ends: losing it for the life of the guest is what turns a passing
		// failure into a VM the host can neither drive nor shut down.
		for {
			if err := guestexec.ListenAndServe(context.Background(), children, logf); err != nil {
				logf("WARNING: exec service stopped: %v", err)
			}
			time.Sleep(serviceRestartDelay)
		}
	}()
	logf("exec service listening on vsock port %d", guestexec.Port)
}

// serveControl starts the vsock control service, which is how the host asks the guest to shut down. The
// Kata kernel has no gpio-keys driver, so Apple VF's RequestStop never reaches the guest and this is the
// only clean way to halt it.
func serveControl() {
	handler := guestctl.Handler{
		Shutdown: func() error {
			logf("shutdown requested by the host")
			return poweroff()
		},
	}
	go func() {
		for {
			if err := guestctl.ListenAndServe(context.Background(), handler, logf); err != nil {
				logf("WARNING: control service stopped: %v", err)
			}
			time.Sleep(serviceRestartDelay)
		}
	}()
	logf("control service listening on vsock port %d", guestctl.Port)
}

// bringUp is the boot sequence proper: pseudo filesystems, the overlay root, the pivot, then the session's
// setup.
func bringUp(children *processTable) error {
	// Set init's own environment first. exec.Command resolves a bare binary name against the CURRENT
	// process's PATH (not cmd.Env), so this has to happen before anything is launched, and children then
	// inherit it for free.
	for k, v := range defaultEnv() {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}
	// The vsock services are the only way into a VM, and their accept loops fail once this process runs
	// out of descriptors.
	if err := raiseFileLimit(); err != nil {
		logf("WARNING: could not raise the open file limit: %v", err)
	}
	if err := mountPseudo(); err != nil {
		return err
	}
	logf("pseudo filesystems mounted")

	// Fetch the session's configuration before building the overlay, because it carries the overlay's
	// size cap. vsock only needs /dev, which is mounted above, so this works well before networking.
	hostCfg, err := fetchConfig()
	if err != nil {
		return fmt.Errorf("fetch session config: %w", err)
	}
	// The session's environment goes on THIS process, not just the entrypoint's: deepcove starts the
	// agent over the exec channel, and those children inherit from init. Setting it only on the
	// entrypoint left the agent without ANTHROPIC_BASE_URL, LANG and the rest.
	for k, v := range hostCfg.Env {
		if err := os.Setenv(k, v); err != nil {
			logf("WARNING: could not set %s: %v", k, err)
		}
	}

	if err := setupOverlayRoot(hostCfg.OverlayBytes); err != nil {
		return err
	}
	logf("overlay root assembled (writable layer cap: %s)", overlayCapLabel(hostCfg.OverlayBytes))
	if err := pivotInto(newRoot); err != nil {
		return err
	}
	// Re-mount the pseudo filesystems inside the new root; the pivot left them behind on the old root.
	if err := mountPseudo(); err != nil {
		return err
	}
	logf("pivoted into overlay root")

	// Networking is best effort: a session without it is degraded but still usable, and failing the boot
	// over it would cost the console and exec needed to diagnose it.
	if err := setupNetwork(); err != nil {
		logf("WARNING: network setup failed, the session has no network: %v", err)
	} else if warnings, err := netguard.Apply(hostCfg.NetworkAllow, nil); err != nil {
		// Fail closed. A session with no network is a bad session; one that can still reach the host's
		// LAN and tailnet is the thing this prevents, so the interface goes down instead.
		logf("ERROR: could not restrict the sandbox network, taking it down: %v", err)
		if derr := linkDown(primaryInterface); derr != nil {
			logf("ERROR: could not take the network down either: %v", derr)
		}
	} else {
		logf("network up, private networks blocked%s", allowLabel(hostCfg.NetworkAllow))
		// An address the operator asked for and did not get. The session still runs, so say which.
		for _, w := range warnings {
			logf("WARNING: %s", w)
		}
	}
	// After the guard, so the log reads in the order things happen. It needs no network: this traffic
	// leaves over vsock.
	if hostCfg.LLMRequestPort > 0 {
		if err := serveAgentEndpoint(hostCfg.LLMRequestPort); err != nil {
			logf("WARNING: the session has no LLM endpoint: %v", err)
		} else {
			logf("LLM requests on port %d go to the host", hostCfg.LLMRequestPort)
		}
	}
	mountShares(hostCfg.Mounts)
	mountHome(hostCfg)
	if hostCfg.SwapDevice != "" {
		if err := enableSwap(hostCfg.SwapDevice); err != nil {
			logf("WARNING: no swap, a large write can still exhaust memory: %v", err)
		} else {
			logf("swap enabled on %s", hostCfg.SwapDevice)
		}
	}

	// Before the entrypoint, which starts the agent, so it finds the shared login already there.
	keepSharedLogin(hostCfg.SharedLoginAgent)

	return runEntrypoint(children, hostCfg)
}

// mountHome mounts the session's persistent home when the disk already carries a filesystem.
//
// A disk on its first boot is raw, so this fails and the entrypoint formats it instead. Mounting is
// attempted rather than probed because the base image ships no blkid-free way to ask, and a failed mount
// is the same answer at no cost.
func mountHome(cfg guestcfg.Config) {
	if cfg.HomeDevice == "" {
		return
	}
	if err := os.MkdirAll(sandbox.SandboxedAgentHome, 0o700); err != nil {
		logf("WARNING: could not create %s: %v", sandbox.SandboxedAgentHome, err)
		return
	}
	if err := unix.Mount(cfg.HomeDevice, sandbox.SandboxedAgentHome, "ext4", 0, ""); err != nil {
		logf("home disk is not formatted yet; the entrypoint will prepare it")
		return
	}
	logf("persistent home mounted at %s", sandbox.SandboxedAgentHome)
}

// mountShares mounts the host directories this VM was given, after the pivot so they land inside the
// session's root. Best effort per mount: a share that fails leaves the session usable without it, which
// beats refusing to boot over one bad path.
func mountShares(mounts []guestcfg.Mount) {
	for _, m := range mounts {
		if err := os.MkdirAll(m.Target, 0o755); err != nil {
			logf("WARNING: could not create %s: %v", m.Target, err)
			continue
		}
		var flags uintptr
		if m.ReadOnly {
			flags = unix.MS_RDONLY
		}
		if err := unix.Mount(m.Tag, m.Target, "virtiofs", flags, ""); err != nil {
			logf("WARNING: could not mount %s at %s: %v", m.Tag, m.Target, err)
			continue
		}
		logf("mounted %s at %s", m.Target, m.Tag)
	}
}

// runEntrypoint runs the session's entrypoint: it performs the agent setup (installing tmux and the agent
// when the image lacks them) and writes the agent's launch script. The host starts the agent afterwards,
// in a tmux session of its own.
func runEntrypoint(children *processTable, hostCfg guestcfg.Config) error {
	argv := hostCfg.Entrypoint
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv comes from the trusted host side
	cmd.Env = append(os.Environ(), envList(hostCfg.Env)...)
	// The entrypoint formats the home disk on its first boot, so it has to know where it is. Absent for
	// a VM without a persistent home, which leaves the entrypoint's block inert.
	if hostCfg.HomeDevice != "" {
		// The entrypoint script formats and mounts the home disk itself on first boot, so it is told where.
		cmd.Env = append(cmd.Env,
			"DEEPCOVE_HOME_DEVICE="+hostCfg.HomeDevice,
			"DEEPCOVE_HOME_TARGET="+sandbox.SandboxedAgentHome)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Run it in the background: init stays alive on its own and has to get on with serving vsock. Through
	// the process table, because the reaper would otherwise take its exit status out from under cmd.Wait.
	if err := children.Start(cmd, cmd.Start); err != nil {
		return fmt.Errorf("start entrypoint: %w", err)
	}
	go func() {
		code, err := children.Wait(cmd)
		if err != nil {
			logf("session entrypoint finished, exit code unknown: %v", err)
			return
		}
		logf("session entrypoint finished: exit code %d", code)
	}()
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
func holdConsole(children *processTable) {
	for _, sh := range shellCandidates() {
		cmd := exec.Command(sh) //nolint:gosec // fixed list of shell paths, no user input
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err == nil {
			logf("console shell exited; holding the guest open")
			break
		}
	}
	children.reapForever()
}

// logf writes a progress line to the console (stdout is the virtio serial port), so a real boot is
// observable and a failing step is obvious.
func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stdout, "[deepcove-init] "+format+"\n", args...)
}

// raiseFileLimit lifts the soft open file limit to the kernel's hard limit.
func raiseFileLimit() error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return err
	}
	limit.Cur = limit.Max
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &limit)
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
