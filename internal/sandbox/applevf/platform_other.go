//go:build !darwin

package applevf

import (
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func platformVersion() (string, error) {
	return "", fmt.Errorf("applevf: requires macOS: %w", sandbox.ErrNotSupported)
}
