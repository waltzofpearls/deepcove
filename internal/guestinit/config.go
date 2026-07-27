package guestinit

import (
	"os"
	"strings"
)

// LoadConfig reads the per boot configuration baked into the image. The agent launch command lives in a
// plain file at LaunchScriptPath (written during image assembly, mirroring the Docker entrypoint). A
// missing or empty file is not fatal: launchCommand falls back to a shell, so a half configured image
// still boots to a usable console.
func LoadConfig() Config {
	cfg := loadConfigFrom(LaunchScriptPath)
	if agent, err := os.ReadFile(AgentPath); err == nil {
		cfg.Agent = strings.TrimSpace(string(agent))
	}
	return cfg
}

func loadConfigFrom(path string) Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}
	}
	return Config{LaunchCmd: strings.TrimSpace(string(data))}
}
