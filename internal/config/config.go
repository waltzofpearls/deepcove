package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sethvargo/go-envconfig"
)

type Config struct {
	IsDev    bool   `env:"IS_DEV, default=false"`
	Addr     string `env:"ADDR, default=127.0.0.1:8080"`
	DataDir  string `env:"DATA_DIR"`
	NodeName string `env:"NODE_NAME"`
	LogLevel string `env:"LOG_LEVEL"`

	// Cluster is this node's cluster label (SPEC §5.12), also settable with the `--cluster` flag on
	// `deepcove start` and later in the web UI. Empty falls back to the persisted value, then "default".
	Cluster string `env:"DEEPCOVE_CLUSTER"`
	ViteURL string `env:"VITE_URL, default=http://localhost:5173"`

	// Transport forces the SPA's runtime transport ("direct" or "wasm"), and
	// WasmTarget is the deepcove node's tailnet URL the in-browser node dials in
	// wasm mode. Both are dev/test overrides until per-listener selection (the
	// automatic reachability-based choice) lands; empty = direct, no target.
	Transport  string `env:"TRANSPORT"`
	WasmTarget string `env:"WASM_TARGET"`

	// VMKernel and VMGuestInit point at the artifacts the Apple VF runtime needs: the guest Linux
	// kernel and the init binary baked into every session image. The runtime is offered only when both
	// exist, so a node without them simply keeps Docker as its only runtime rather than failing.
	//
	// Provisioning them automatically (embedding the init, fetching the kernel on demand) is still to
	// come; until then these are paths you point at yourself, defaulting to where the Makefile writes.
	VMKernel    string `env:"DEEPCOVE_VM_KERNEL"`
	VMGuestInit string `env:"DEEPCOVE_VM_GUEST_INIT"`

	// DefaultRuntime overrides which runtime a session gets when the create request does not choose
	// one ("docker", "applevf"). Empty prefers the VM runtime wherever it is available, falling back to
	// Docker on nodes that cannot provide VMs.
	DefaultRuntime string `env:"DEEPCOVE_DEFAULT_RUNTIME"`

	// Computed values, should not read from env
	Hostname string
}

const (
	DefaultLogLevel = "info"
)

func Load(ctx context.Context) (*Config, error) {
	var c Config
	if err := envconfig.Process(ctx, &c); err != nil {
		return nil, err
	}
	if c.DataDir == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		// No leading dot. os.UserConfigDir already returns a hidden/conventional location per
		// platform (~/.config on Linux, ~/Library/Application Support on macOS, %AppData% on
		// Windows), and dotfiles inside those are either redundant or anomalous.
		c.DataDir = filepath.Join(configDir, "deepcove")
	}
	if c.NodeName == "" {
		nodeName, err := os.Hostname()
		if err != nil {
			return nil, err
		}
		c.NodeName = nodeName
	}
	c.Hostname = fmt.Sprintf("deepcove-%s", c.NodeName)
	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}
	return &c, nil
}
