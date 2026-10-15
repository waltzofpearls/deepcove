//go:build linux

package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/netguard"
)

// guardNetwork blocks, in the network it runs in, everything a sandbox must not reach, then runs the
// command after `--`, which is passt. pasta starts it inside the private network it made for passt.
//
// It stays for as long as passt runs, because only a process inside that network can change its routes.
// deepcove tells it over the control socket when the host joins a network, and it blocks that one too.
// The socket can only add blocks, so whoever reaches it cannot open a hole.
//
// It fails closed: if the block cannot go in, passt never starts and the VM gets no network at all. And
// if deepcove stops watching, by crashing say, passt is stopped, since nothing would block a new network.
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
	listener, err := net.Listen("unix", request.control)
	if err != nil {
		return fmt.Errorf("vm guard-network: %w", err)
	}
	defer func() { _ = listener.Close() }()

	passt := exec.Command(request.command[0], request.command[1:]...) //nolint:gosec // argv comes from deepcove itself
	passt.Stdin, passt.Stdout, passt.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := passt.Start(); err != nil {
		return fmt.Errorf("vm guard-network: %w", err)
	}
	block := func(networks []netip.Prefix) error {
		_, err := netguard.Apply(request.allowed, networks)
		return err
	}
	stopPasst := func() { _ = passt.Process.Kill() }
	go serveControl(listener, block, stopPasst, deepcoveHoldWait)

	if err := passt.Wait(); err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			os.Exit(exited.ExitCode())
		}
		return fmt.Errorf("vm guard-network: %w", err)
	}
	return nil
}

// holdRequest is how deepcove says it is watching: it sends this, then keeps the connection open.
const holdRequest = "hold"

// deepcoveHoldWait is how long deepcove has to start watching once passt is up. It does so at once.
const deepcoveHoldWait = 30 * time.Second

// serveControl answers deepcove on the control socket. A hold request lasts while deepcove watches the
// host's networks; when it ends, or none comes within holdWait, lost runs. Any other request names
// networks to block.
func serveControl(listener net.Listener, block func([]netip.Prefix) error, lost func(), holdWait time.Duration) {
	noHold := time.AfterFunc(holdWait, lost)
	for {
		conn, err := listener.Accept()
		if err != nil {
			// Without the control socket a network the host joins could not be blocked.
			lost()
			return
		}
		go handleControl(conn, block, func() { noHold.Stop() }, lost)
	}
}

func handleControl(conn net.Conn, block func([]netip.Prefix) error, held, lost func()) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	first, err := reader.ReadString('\n')
	if err == nil && strings.TrimSpace(first) == holdRequest {
		held()
		_, _ = io.Copy(io.Discard, reader)
		lost()
		return
	}
	// Allowed addresses are opened again after the block, so a network the host joins is held to the
	// same rule as the ones it had at boot.
	networks, err := readBlockRequest(io.MultiReader(strings.NewReader(first), reader))
	if err == nil {
		err = block(networks)
	}
	if err != nil {
		_, _ = fmt.Fprintf(conn, "error: %v\n", err)
		return
	}
	_, _ = fmt.Fprintln(conn, "ok")
}

// readBlockRequest reads one network per line until the sender stops writing.
func readBlockRequest(request io.Reader) ([]netip.Prefix, error) {
	var networks []netip.Prefix
	scan := bufio.NewScanner(request)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		network, err := netip.ParsePrefix(line)
		if err != nil {
			return nil, fmt.Errorf("not a network: %q", line)
		}
		networks = append(networks, network.Masked())
	}
	return networks, scan.Err()
}

type guardNetworkRequest struct {
	blocked []netip.Prefix
	allowed []string
	control string
	command []string
}

func parseGuardNetworkArgs(args []string) (guardNetworkRequest, error) {
	fs := flag.NewFlagSet("vm guard-network", flag.ContinueOnError)
	var blocked, allowed repeatedFlag
	var control string
	fs.Var(&blocked, "block", "a network to block besides the private ranges; repeatable")
	fs.Var(&allowed, "allow", "a private address the sandbox may still reach; repeatable")
	fs.StringVar(&control, "control", "", "the socket deepcove sends networks to block on")
	if err := fs.Parse(args); err != nil {
		return guardNetworkRequest{}, err
	}
	request := guardNetworkRequest{allowed: allowed, control: control, command: fs.Args()}
	if len(request.command) == 0 {
		return guardNetworkRequest{}, errors.New("vm guard-network: nothing to run after --")
	}
	if request.control == "" {
		return guardNetworkRequest{}, errors.New("vm guard-network: no --control socket")
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
