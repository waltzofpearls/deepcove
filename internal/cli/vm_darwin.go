//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/waltzofpearls/deepcove/internal/sandbox/applevf"
)

func runVM(ctx context.Context, data []byte) error {
	var cfg applevf.RunConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("vm run: read the settings: %w", err)
	}
	return applevf.Run(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}
