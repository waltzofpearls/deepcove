// Package guestinit is the PID 1 that runs inside an Apple VF (and later Firecracker) session VM. The
// kernel boots the read only ext4 base directly and hands control to the deepcove init binary, baked
// into the image at InitPath. Init overlays a writable layer on the read only root, pivots into it,
// brings up the pseudo filesystems, and launches the agent under tmux with the same session shape the
// Docker runtime uses, so attach and preview work unchanged.
//
// The syscall choreography lives in run_linux.go and can only be exercised inside a real guest boot. The
// pure decisions (overlay options, the tmux launch argv, the mount table) live here so they are unit
// tested on any OS, including the Linux CI runner.
package guestinit

import (
	"errors"
	"strings"
	"syscall"
)

// InitPath is where the init binary is baked into the rootfs, and what the kernel command line's
// init= points at. Kept in sync with defaultCmdline in the applevf package.
const InitPath = "/sbin/deepcove-init"

// LaunchScriptPath is the shell fragment (written into the image at build time) that starts the agent.
// Init runs it as the tmux "main" session's command, mirroring the Docker entrypoint. Exported so the
// image builder knows where to inject it.
const LaunchScriptPath = "/etc/deepcove/agent-launch"

// AgentPath holds the agent name, which tells the guest which credential bundle to broker with the host.
// Absent means this session brokers nothing (the work profile case).
const AgentPath = "/etc/deepcove/agent"

// OverlayMountDir is baked into the image as an empty directory at build time, because the base is
// mounted read only and init therefore cannot mkdir anything at runtime. Init mounts a tmpfs here, which
// makes it writable, and builds the whole overlay inside it.
const OverlayMountDir = "/deepcove"

// Overlay layout, all inside the tmpfs at OverlayMountDir. The read only root is used directly as the
// overlay lowerdir, so no bind mount is needed. The merged result is assembled at newRoot before
// pivoting into it.
const (
	upperDir = OverlayMountDir + "/upper"
	workDir  = OverlayMountDir + "/work"
	newRoot  = OverlayMountDir + "/newroot"
)

// tmuxSession and tmuxWindow match the Docker runtime so session/attach.go and session/preview.go, which
// target "main", work identically over the VF runtime.
const (
	tmuxSession = "main"
	tmuxWindow  = "main"
)

// overlaySpec names the three overlayfs directories. mountData renders the -o option string.
type overlaySpec struct {
	Lower string
	Upper string
	Work  string
}

// defaultOverlay layers the writable tmpfs upper over the read only root. Using "/" directly as the
// lowerdir avoids needing a bind mount, and therefore avoids needing another baked mount point.
func defaultOverlay() overlaySpec {
	return overlaySpec{Lower: "/", Upper: upperDir, Work: workDir}
}

func (o overlaySpec) mountData() string {
	return "lowerdir=" + o.Lower + ",upperdir=" + o.Upper + ",workdir=" + o.Work
}

// tmuxLaunch is the argv that starts the agent in a detached tmux session named "main", the exact shape
// the Docker runtime uses. cmd is the shell command that runs the agent (from launchScriptPath).
func tmuxLaunch(cmd string) []string {
	return []string{"tmux", "new-session", "-d", "-s", tmuxSession, "-n", tmuxWindow, cmd}
}

// pseudoMount is one kernel filesystem the guest must mount before anything else works.
type pseudoMount struct {
	Source string
	Target string
	FSType string
	Flags  uintptr
	Data   string
}

// pseudoMounts is the minimal set for a usable userspace, in dependency order: /dev/pts and /dev/shm sit
// inside /dev, so devtmpfs has to be mounted first.
func pseudoMounts() []pseudoMount {
	return []pseudoMount{
		{Source: "proc", Target: "/proc", FSType: "proc"},
		{Source: "sysfs", Target: "/sys", FSType: "sysfs"},
		{Source: "devtmpfs", Target: "/dev", FSType: "devtmpfs"},
		// devpts backs pseudo terminals. Without it tmux cannot open a pty and dies with
		// "create window failed: fork failed: No such file or directory". gid=5 is the conventional tty
		// group and mode=620 makes a new pty owned by the user and writable by that group.
		{Source: "devpts", Target: "/dev/pts", FSType: "devpts", Data: "gid=5,mode=620"},
		// Shared memory, which many runtimes and language toolchains expect to exist.
		{Source: "tmpfs", Target: "/dev/shm", FSType: "tmpfs", Data: "mode=1777"},
	}
}

// Interface names inside the guest. vmnet NAT presents a single virtio network device, which Linux names
// eth0, and loopback has to be up for anything that talks to itself.
const (
	loopbackInterface = "lo"
	primaryInterface  = "eth0"
)

// dhcpCommand is one DHCP client invocation to try.
type dhcpCommand struct {
	Path string
	Args []string
}

// dhcpCandidates are the DHCP clients init tries, in order. busybox udhcpc covers the alpine based agent
// images; dhclient and dhcpcd cover debian and other bases. All absolute, because this can run before a
// usable PATH is guaranteed.
func dhcpCandidates() []dhcpCommand {
	return []dhcpCommand{
		// -q exits once the lease is obtained rather than staying in the foreground, which is what lets
		// init continue booting.
		{Path: "/sbin/udhcpc", Args: []string{"-i", primaryInterface, "-q", "-n"}},
		{Path: "/usr/sbin/dhclient", Args: []string{"-1", primaryInterface}},
		{Path: "/sbin/dhcpcd", Args: []string{"-1", primaryInterface}},
	}
}

// shellCandidates are the shells init tries, in order, when it has to drop to a console. Container
// images vary: busybox based ones (alpine) have /bin/sh, glibc ones usually add bash. All absolute,
// because PATH lookups are unreliable this early (see defaultEnv).
func shellCandidates() []string {
	return []string{"/bin/sh", "/bin/bash", "/busybox/sh"}
}

// defaultEnv is the environment init installs for itself and therefore for every process it starts.
//
// The kernel hands PID 1 an almost empty environment with no PATH at all, so a bare command name like
// "tmux" fails to resolve even when the binary is present in the image. Docker never hits this because
// it sets PATH from the image config; a direct kernel boot has no such step, so init must do it.
func defaultEnv() map[string]string {
	return map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": "/root",
		"TERM": "xterm-256color",
		// C.UTF-8 keeps tmux, less, and git pagers from substituting non ASCII bytes, matching what the
		// agent image sets for the Docker runtime.
		"LANG": "C.UTF-8",
	}
}

// mountAlreadyDone reports whether a mount error just means the target is already mounted. The Kata
// kernel sets CONFIG_DEVTMPFS_MOUNT=y, so it mounts /dev before init ever runs and our own mount comes
// back EBUSY. Treating that as success is required: returning the error kills init and panics the guest.
func mountAlreadyDone(err error) bool {
	return errors.Is(err, syscall.EBUSY)
}

// agentHome is where the agent keeps its state and credentials inside the guest, matching the /root
// volume the Docker runtime mounts.
const agentHome = "/root"

// credentialPaths are the files brokered between the host and the guest, relative to agentHome. Keeping
// this an explicit list means a session hands back exactly its login state, not whatever else the agent
// happened to leave in its home directory.
func credentialPaths() []string {
	return []string{
		".claude/.credentials.json",
		".claude.json",
	}
}

// Config is the per boot input to Run, populated by the init binary's main from files baked into the
// image. LaunchCmd is the agent command run under tmux; empty falls back to an interactive shell so a
// misconfigured image still yields a usable console rather than a panic.
type Config struct {
	LaunchCmd string
	// Agent names which credential bundle to broker with the host. Empty means this session brokers
	// nothing, which is the work profile case where the gateway supplies credentials instead.
	Agent string
}

func (c Config) launchCommand() string {
	if strings.TrimSpace(c.LaunchCmd) == "" {
		return "/bin/sh"
	}
	return c.LaunchCmd
}
