//go:build !linux

package cli

import "errors"

func guardNetwork([]string) error {
	return errors.New("vm guard-network: only used on Linux")
}
