//go:build !darwin

package cli

import (
	"context"
	"errors"
)

func runVM(context.Context, []byte) error {
	return errors.New("vm run: only used on macOS")
}
