//go:build linux

package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/netguard"
)

// guardNetwork blocks, in the network it runs in, everything a sandbox must not reach, then becomes the
// command after `--`, which is passt. pasta starts it inside the private network it made for passt.
//
// It fails closed: if the block cannot go in, passt never starts and the VM gets no network at all.
func guardNetwork(args []string) error {
	request, err := parseGuardNetworkArgs(args)
	if err != nil {
		return err
	}
	warnings, err := netguard.Apply(request.allowed, request.blocked)
	if err != nil {
		return fmt.Errorf("vm guard-network: %w", err)
	}
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "vm guard-network:", warning)
	}
	return unix.Exec(request.command[0], request.command, os.Environ())
}

type guardNetworkRequest struct {
	blocked []netip.Prefix
	allowed []string
	command []string
}

func parseGuardNetworkArgs(args []string) (guardNetworkRequest, error) {
	fs := flag.NewFlagSet("vm guard-network", flag.ContinueOnError)
	var blocked, allowed repeatedFlag
	fs.Var(&blocked, "block", "a network to block besides the private ranges; repeatable")
	fs.Var(&allowed, "allow", "a private address the sandbox may still reach; repeatable")
	if err := fs.Parse(args); err != nil {
		return guardNetworkRequest{}, err
	}
	request := guardNetworkRequest{allowed: allowed, command: fs.Args()}
	if len(request.command) == 0 {
		return guardNetworkRequest{}, errors.New("vm guard-network: nothing to run after --")
	}
	for _, raw := range blocked {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return guardNetworkRequest{}, fmt.Errorf("vm guard-network: --block %q: %w", raw, err)
		}
		request.blocked = append(request.blocked, prefix.Masked())
	}
	return request, nil
}
