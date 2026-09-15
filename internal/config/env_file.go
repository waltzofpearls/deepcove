package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFileName is where a node keeps settings that have to survive a restart, next to its database. A
// background service reads them from here because it has no terminal to inherit them from, and because
// a service definition is world readable and this file is not.
const envFileName = "env"

// envFileMode keeps the settings to their owner. A token is usually one of them.
const envFileMode = 0o600

// EnvFileOpenToOthers reports whether anyone but the owner can read the settings. Worth saying out loud,
// since the file may hold a token and deepcove is not the only thing that can write it.
func EnvFileOpenToOthers(dataDir string) (bool, error) {
	info, err := os.Stat(EnvFilePath(dataDir))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: %w", err)
	}
	return info.Mode().Perm()&0o077 != 0, nil
}

// EnvFilePath is the file itself, so a command can tell you where its settings went.
func EnvFilePath(dataDir string) string {
	return filepath.Join(dataDir, envFileName)
}

// loadEnvFile puts the file's settings into this process's environment. A variable already set wins, so
// what you export for one run still beats what is stored. A missing file is normal, not an error.
func loadEnvFile(dataDir string) error {
	text, err := os.ReadFile(EnvFilePath(dataDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: read %s: %w", EnvFilePath(dataDir), err)
	}
	for _, line := range strings.Split(string(text), "\n") {
		key, value, ok := parseEnvLine(line)
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("config: set %s: %w", key, err)
		}
	}
	return nil
}

// SetEnvFileValues stores KEY=VALUE entries, replacing the ones already there and keeping everything
// else, comments included. The file is readable only by its owner, since a token may live in it.
func SetEnvFileValues(dataDir string, entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	path := EnvFilePath(dataDir)
	text, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	for _, entry := range entries {
		key, value, ok := parseEnvLine(entry)
		if !ok {
			return fmt.Errorf("config: %q is not KEY=VALUE", entry)
		}
		lines = setEnvLine(lines, key, value)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), envFileMode); err != nil {
		return err
	}
	// WriteFile only applies the mode to a file it creates, and this one usually exists already.
	return os.Chmod(path, envFileMode)
}

// UnsetEnvFileValues drops these keys from the file. A key that was never there is not an error: you
// asked for it gone, and it is gone.
func UnsetEnvFileValues(dataDir string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	path := EnvFilePath(dataDir)
	text, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}

	drop := make(map[string]bool, len(keys))
	for _, key := range keys {
		drop[strings.TrimSpace(key)] = true
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if key, _, ok := parseEnvLine(line); ok && drop[key] {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// EnvFileValues is what is stored, in the order the file holds it, as "KEY=VALUE" entries.
func EnvFileValues(dataDir string) ([]string, error) {
	text, err := os.ReadFile(EnvFilePath(dataDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", EnvFilePath(dataDir), err)
	}
	var out []string
	for _, line := range strings.Split(string(text), "\n") {
		if key, value, ok := parseEnvLine(line); ok {
			out = append(out, key+"="+value)
		}
	}
	return out, nil
}

// setEnvLine replaces the line holding this key, or adds one when the key is new.
func setEnvLine(lines []string, key, value string) []string {
	for i, line := range lines {
		if have, _, ok := parseEnvLine(line); ok && have == key {
			lines[i] = key + "=" + value
			return lines
		}
	}
	return append(lines, key+"="+value)
}

// parseEnvLine reads one KEY=VALUE line. Blank lines, comments and anything without a name are not
// settings, and the caller skips them.
func parseEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, value, found := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}
