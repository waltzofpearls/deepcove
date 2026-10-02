//go:build linux

package guestinit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// hostContextID is the well known vsock address of the host, which is what the guest dials to reach the
// credential broker.
const hostContextID uint32 = 2

// fetchCredentials pulls the agent's stored credentials from the host and writes them under the agent
// home. They land on the overlay's tmpfs upper layer, so they exist only for the life of this VM and
// never touch the read only base image.
//
// Not having any is normal: it means the agent has not logged in yet, and the session should start so
// the user can do exactly that.
func fetchCredentials(cfg Config) error {
	if cfg.Agent == "" {
		return nil // nothing to fetch for a profile that uses gateway injected credentials
	}
	conn, err := vsock.Dial(hostContextID, credbroker.Port, nil)
	if err != nil {
		return fmt.Errorf("dial credential broker: %w", err)
	}
	defer func() { _ = conn.Close() }()

	files, err := credbroker.Fetch(conn, cfg.Agent)
	if errors.Is(err, credbroker.ErrNoCredentials) {
		logf("no stored credentials for %q yet; the agent will need to log in", cfg.Agent)
		return nil
	}
	if err != nil {
		return err
	}
	if err := writeCredentialFiles(credbroker.KeepAgentFiles(cfg.Agent, files)); err != nil {
		return err
	}
	logf("restored %d credential file(s) for %q", len(files), cfg.Agent)
	return nil
}

// credentialPollInterval is how often the guest checks whether the agent's login changed. Logging in is
// a human paced action, so a few seconds is responsive, and reading two small files costs nothing.
const credentialPollInterval = 5 * time.Second

// watchCredentials pushes the agent's login to the host whenever it changes, for the life of the VM.
//
// Pushing only at shutdown is not enough: sessions run concurrently, so logging in inside one VM has to
// reach the host while that VM is still alive, or a session created afterwards starts logged out. VMs
// cannot share a disk, so the guest has to tell the host.
func watchCredentials(cfg Config) {
	if cfg.Agent == "" {
		return // a gateway credential profile brokers nothing
	}
	var lastPushed string
	for {
		time.Sleep(credentialPollInterval)

		files, err := readCredentialFiles(cfg.Agent)
		if err != nil || !credbroker.HasLogin(cfg.Agent, files) {
			// No login here to share. Never push one: it would wipe a good login on the host, and a
			// logout in one session is not a logout everywhere.
			continue
		}
		sum := fingerprint(files)
		if sum == lastPushed {
			continue
		}
		if err := pushCredentials(cfg.Agent, files); err != nil {
			logf("WARNING: could not push credentials: %v", err)
			continue
		}
		lastPushed = sum
		logf("pushed %d credential file(s) for %q to the host", len(files), cfg.Agent)
	}
}

// fingerprint identifies a bundle's contents so unchanged credentials are not pushed repeatedly.
func fingerprint(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names) // map order is random; the fingerprint must not be
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pushCredentials sends one bundle to the host's broker.
func pushCredentials(agent string, files map[string][]byte) error {
	conn, err := vsock.Dial(hostContextID, credbroker.Port, nil)
	if err != nil {
		return fmt.Errorf("dial credential broker: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return credbroker.StoreFiles(conn, agent, files)
}

// returnCredentials hands whatever the agent refreshed back to the host, at shutdown. The watcher above
// already pushes on change, so this is a final flush for anything written in the last few seconds.
func returnCredentials(cfg Config) error {
	if cfg.Agent == "" {
		return nil
	}
	files, err := readCredentialFiles(cfg.Agent)
	if err != nil || !credbroker.HasLogin(cfg.Agent, files) {
		return err
	}
	if err := pushCredentials(cfg.Agent, files); err != nil {
		return err
	}
	logf("returned %d credential file(s) for %q", len(files), cfg.Agent)
	return nil
}

// writeCredentialFiles materializes a bundle under the agent home, creating parents as needed. Files are
// owner only because they are secrets.
func writeCredentialFiles(files map[string][]byte) error {
	for rel, data := range files {
		path := filepath.Join(sandbox.SandboxedAgentHome, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// readCredentialFiles collects the agent's credential files for the trip back to the host. Missing files
// are skipped rather than treated as an error, since the agent may never have created them.
func readCredentialFiles(agent string) (map[string][]byte, error) {
	var files map[string][]byte
	for _, rel := range credbroker.AgentFiles(agent) {
		data, err := os.ReadFile(filepath.Join(sandbox.SandboxedAgentHome, rel))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		if files == nil {
			files = make(map[string][]byte)
		}
		files[rel] = data
	}
	return files, nil
}
