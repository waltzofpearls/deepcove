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
	ViteURL  string `env:"VITE_URL, default=http://localhost:5173"`

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
		// No leading dot — os.UserConfigDir already returns a
		// hidden/conventional location per platform (~/.config on
		// Linux, ~/Library/Application Support on macOS,
		// %AppData% on Windows), and dotfiles inside those are
		// either redundant or anomalous.
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
