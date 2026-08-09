//go:build !darwin

package applevf

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func serveLLMForward(*machine, func() string, func() *http.Client, *slog.Logger) error {
	return fmt.Errorf("applevf: forwarding LLM requests requires macOS: %w", sandbox.ErrNotSupported)
}
