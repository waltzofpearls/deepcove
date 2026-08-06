//go:build !darwin

package applevf

import (
	"context"
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func (m *machine) waitReady(context.Context, time.Duration) error {
	return fmt.Errorf("applevf: requires macOS: %w", sandbox.ErrNotSupported)
}

func (m *machine) captureConsole() {}
