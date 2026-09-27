package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sethvargo/go-envconfig"
)

type Config struct {
	Addr     string `env:"ADDR, default=127.0.0.1:9000"`
	DataDir  string `env:"DATA_DIR"`
	NodeName string `env:"NODE_NAME"`
	LogLevel string `env:"LOG_LEVEL"`

	// Transport forces the SPA's runtime transport ("direct" or "wasm"), and
	// WasmTarget is the deepcove node's tailnet URL the in-browser node dials in
	// wasm mode. Both are dev/test overrides until per-listener selection (the
	// automatic reachability-based choice) lands; empty = direct, no target.
	Transport  string `env:"TRANSPORT"`
	WasmTarget string `env:"WASM_TARGET"`

	// VMKernel and VMGuestInit point at the artifacts the Apple VF runtime needs: the guest Linux
	// kernel and the init binary baked into every session image. Set either one to boot a file you
	// built yourself, and it wins over anything the node would otherwise use.
	VMKernel    string `env:"DEEPCOVE_VM_KERNEL"`
	VMGuestInit string `env:"DEEPCOVE_VM_GUEST_INIT"`

	// LocalVMAssets boots the VM artifacts built in this checkout rather than the ones downloaded from
	// this build's own release. For a developer testing a kernel they just built; a user never sets it.
	LocalVMAssets bool `env:"DEEPCOVE_LOCAL_VM_ASSETS"`

	// DefaultRuntime overrides which runtime a session gets when the create request does not choose
	// one ("applevf", "cloudhv", "sandboxless"). Empty prefers the VM runtime wherever it is available;
	// a node without one then has no default, and sandboxless has to be named on each create.
	DefaultRuntime string `env:"DEEPCOVE_DEFAULT_RUNTIME"`

	// AllowSandboxHosts are private addresses a sandbox may still reach, which are otherwise blocked.
	// Command line only, on purpose: no env tag, no API, no stored setting, so there is nothing a public
	// build could read to open one.
	AllowSandboxHosts []string

	// Computed values, should not read from env
	Hostname string
}

const (
	DefaultLogLevel = "info"
)

// defaultDataDir is where a node keeps everything when DATA_DIR names nowhere.
//
// No leading dot. os.UserConfigDir already returns a hidden/conventional location per platform
// (~/.config on Linux, ~/Library/Application Support on macOS, %AppData% on Windows), and dotfiles
// inside those are either redundant or anomalous.
func defaultDataDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "deepcove"), nil
}

// dataDirFromEnv answers where the data dir is before the config is loaded, which is when the stored
// settings still have to be read. Nowhere to look is not an error here: the settings are simply not
// found, and Load reports the same problem properly a moment later.
func dataDirFromEnv() string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}
	dir, err := defaultDataDir()
	if err != nil {
		return ""
	}
	return dir
}

func Load(ctx context.Context) (*Config, error) {
	// Before anything reads the environment: the stored settings are part of it, and where they live is
	// the one thing that cannot come from them.
	if err := loadEnvFile(dataDirFromEnv()); err != nil {
		return nil, err
	}
	var c Config
	if err := envconfig.Process(ctx, &c); err != nil {
		return nil, err
	}
	if c.DataDir == "" {
		dir, err := defaultDataDir()
		if err != nil {
			return nil, err
		}
		c.DataDir = dir
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
