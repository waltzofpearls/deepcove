package guestinit

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestOverlayMountData(t *testing.T) {
	got := defaultOverlay().mountData()
	want := "lowerdir=/,upperdir=/deepcove/upper,workdir=/deepcove/work"
	if got != want {
		t.Errorf("mountData() = %q, want %q", got, want)
	}
}

// The overlay directories must all live under the single baked mount point, because that is the only
// place a tmpfs makes writable. Anything outside it would hit the read only base and fail.
func TestOverlayDirsLiveUnderTheBakedMountPoint(t *testing.T) {
	for _, d := range []string{upperDir, workDir, newRoot} {
		if !strings.HasPrefix(d, OverlayMountDir+"/") {
			t.Errorf("%q must live under the baked mount point %q", d, OverlayMountDir)
		}
	}
	// The read only root is the lower layer directly, so no bind mount is needed.
	if got := defaultOverlay().Lower; got != "/" {
		t.Errorf("lowerdir = %q, want / (the read only root used directly)", got)
	}
}

func TestPseudoMountsCoverEssentials(t *testing.T) {
	var targets []string
	for _, m := range pseudoMounts() {
		targets = append(targets, m.Target)
	}
	// /dev/pts is not optional: tmux cannot allocate a pty without it, so the agent never starts.
	for _, want := range []string{"/proc", "/sys", "/dev", "/dev/pts"} {
		if !slices.Contains(targets, want) {
			t.Errorf("pseudoMounts missing %s (got %v)", want, targets)
		}
	}
}

// Mounts nested under /dev must come after devtmpfs, or their mount point does not exist yet.
func TestPseudoMountsAreInDependencyOrder(t *testing.T) {
	seen := map[string]int{}
	for i, m := range pseudoMounts() {
		seen[m.Target] = i
	}
	dev, ok := seen["/dev"]
	if !ok {
		t.Fatal("/dev must be mounted")
	}
	for _, nested := range []string{"/dev/pts", "/dev/shm"} {
		if at, ok := seen[nested]; ok && at < dev {
			t.Errorf("%s is mounted before /dev; its mount point would not exist yet", nested)
		}
	}
}

// Regression: the Kata kernel sets CONFIG_DEVTMPFS_MOUNT=y and mounts /dev before init runs, so our own
// devtmpfs mount returns EBUSY. Treating that as a failure made init exit and panicked the guest with
// "Attempted to kill init". PUI PUI's kernel lacks that option, which is why the first boot test missed it.
func TestMountAlreadyDone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"already mounted", syscall.EBUSY, true},
		{"wrapped already mounted", fmt.Errorf("mount devtmpfs: %w", syscall.EBUSY), true},
		{"no such device", syscall.ENODEV, false},
		{"permission denied", syscall.EPERM, false},
		{"unrelated error", errors.New("boom"), false},
		{"no error", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mountAlreadyDone(tc.err); got != tc.want {
				t.Errorf("mountAlreadyDone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Init must never exit: the kernel panics with "Attempted to kill init" if PID 1 dies, which buries the
// real error. So there must always be a shell to fall back to, and /bin/sh must be first because every
// container image has one.
func TestShellCandidates(t *testing.T) {
	got := shellCandidates()
	if len(got) == 0 {
		t.Fatal("there must be at least one fallback shell")
	}
	if got[0] != "/bin/sh" {
		t.Errorf("first candidate = %q, want /bin/sh (universally present)", got[0])
	}
	for _, sh := range got {
		if !strings.HasPrefix(sh, "/") {
			t.Errorf("candidate %q must be an absolute path; $PATH is unreliable in early boot", sh)
		}
	}
}

// Regression: the kernel gives PID 1 no PATH, so `tmux` failed to resolve even though the agent image
// installs it. Absolute paths like /bin/sh worked, bare names did not. Init must set PATH itself.
func TestDefaultEnvSetsPath(t *testing.T) {
	env := defaultEnv()

	path, ok := env["PATH"]
	if !ok {
		t.Fatal("PATH must be set; without it a bare command name cannot be resolved at all")
	}
	for _, want := range []string{"/usr/bin", "/bin"} {
		if !strings.Contains(path, want) {
			t.Errorf("PATH %q must include %q", path, want)
		}
	}
	if env["HOME"] == "" {
		t.Error("HOME must be set; the agent launch command expands $HOME")
	}
	if env["TERM"] == "" {
		t.Error("TERM must be set; tmux refuses to start without one")
	}
}

// DHCP runs before a usable PATH is guaranteed, so every client path must be absolute, and each must
// target the interface vmnet actually presents.
func TestDHCPCandidates(t *testing.T) {
	got := dhcpCandidates()
	if len(got) == 0 {
		t.Fatal("there must be at least one DHCP client to try")
	}
	for _, c := range got {
		if !strings.HasPrefix(c.Path, "/") {
			t.Errorf("candidate %q must be an absolute path", c.Path)
		}
		if !slices.Contains(c.Args, primaryInterface) {
			t.Errorf("%s args %v must name the primary interface %q", c.Path, c.Args, primaryInterface)
		}
	}
	if loopbackInterface == primaryInterface {
		t.Error("loopback and primary must be different interfaces")
	}
}

// Credential paths are relative to the agent home and are brokered verbatim, so an absolute path here
// would escape the home directory when joined.
func TestCredentialPathsAreRelative(t *testing.T) {
	got := credentialPaths()
	if len(got) == 0 {
		t.Fatal("at least one credential path must be brokered")
	}
	for _, p := range got {
		if strings.HasPrefix(p, "/") {
			t.Errorf("credential path %q must be relative to the agent home, not absolute", p)
		}
		if strings.Contains(p, "..") {
			t.Errorf("credential path %q must not escape the agent home", p)
		}
	}
	// The agent home has to match the /root volume the Docker runtime mounts, or a session would look
	// for its login in a different place depending on the runtime.
	if agentHome != "/root" {
		t.Errorf("agentHome = %q, want /root to match the Docker runtime", agentHome)
	}
}

func TestTmpfsSizeOption(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"uncapped renders no option, leaving the kernel default", 0, ""},
		{"a negative cap is treated as no cap rather than a mount error", -1, ""},
		{"a cap renders as a byte count, the form mount understands", 512 << 20, "size=536870912"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmpfsSizeOption(tc.bytes); got != tc.want {
				t.Errorf("tmpfsSizeOption(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

func TestOverlayCapLabel(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"uncapped says so, rather than printing a misleading zero", 0, "default (half the guest's RAM)"},
		{"a cap is reported in the units it was requested in", 2 << 30, "2048MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlayCapLabel(tc.bytes); got != tc.want {
				t.Errorf("overlayCapLabel(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

// A bundle is only worth pushing when it carries a login. A logged out Claude leaves the credentials
// file in place with empty tokens, and pushing that wiped every session's login on the host.
func TestBundleHasLogin(t *testing.T) {
	const settings = `{"onboarding":true}`
	cases := []struct {
		name  string
		files map[string][]byte
		want  bool
	}{
		{
			name:  "a real login",
			files: map[string][]byte{credentialFile: []byte(`{"claudeAiOauth":{"accessToken":"sk-x","refreshToken":"rt-x"}}`)},
			want:  true,
		},
		{
			name:  "only a refresh token still logs in",
			files: map[string][]byte{credentialFile: []byte(`{"claudeAiOauth":{"accessToken":"","refreshToken":"rt-x"}}`)},
			want:  true,
		},
		{
			name:  "empty tokens are a logged out shell",
			files: map[string][]byte{credentialFile: []byte(`{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0}}`)},
			want:  false,
		},
		{
			name:  "settings without the credentials file",
			files: map[string][]byte{".claude.json": []byte(settings)},
			want:  false,
		},
		{
			name:  "unreadable json",
			files: map[string][]byte{credentialFile: []byte(`not json`)},
			want:  false,
		},
		{name: "nothing at all", files: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bundleHasLogin(tc.files); got != tc.want {
				t.Errorf("bundleHasLogin = %v, want %v", got, tc.want)
			}
		})
	}
}
