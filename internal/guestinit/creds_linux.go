//go:build linux

package guestinit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdlayher/vsock"
	"github.com/waltzofpearls/deepcove/internal/credbroker"
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
	if err := writeCredentialFiles(files); err != nil {
		return err
	}
	logf("restored %d credential file(s) for %q", len(files), cfg.Agent)
	return nil
}

// returnCredentials hands whatever the agent refreshed back to the host, at shutdown, so the next VM
// starts already logged in.
func returnCredentials(cfg Config) error {
	if cfg.Agent == "" {
		return nil
	}
	files, err := readCredentialFiles()
	if err != nil || len(files) == 0 {
		return err
	}
	conn, err := vsock.Dial(hostContextID, credbroker.Port, nil)
	if err != nil {
		return fmt.Errorf("dial credential broker: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := credbroker.StoreFiles(conn, cfg.Agent, files); err != nil {
		return err
	}
	logf("returned %d credential file(s) for %q", len(files), cfg.Agent)
	return nil
}

// writeCredentialFiles materializes a bundle under the agent home, creating parents as needed. Files are
// owner only because they are secrets.
func writeCredentialFiles(files map[string][]byte) error {
	for rel, data := range files {
		path := filepath.Join(agentHome, rel)
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
func readCredentialFiles() (map[string][]byte, error) {
	var files map[string][]byte
	for _, rel := range credentialPaths() {
		data, err := os.ReadFile(filepath.Join(agentHome, rel))
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
