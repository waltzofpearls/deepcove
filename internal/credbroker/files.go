package credbroker

import "encoding/json"

// agentFiles are the login files brokered for each agent, relative to its home. Both sides keep only
// these, so a guest cannot use the shared bundle to plant any other file in later sessions. Only the
// credentials file: Claude's .claude.json can hold MCP server commands, which later sessions would run.
var agentFiles = map[string][]string{
	"claude": {claudeCredentials},
}

// AgentFiles returns the login files brokered for agent, or nil for an agent with none.
func AgentFiles(agent string) []string { return agentFiles[agent] }

// KeepAgentFiles drops every file from a bundle that is not one of agent's login files.
func KeepAgentFiles(agent string, files map[string][]byte) map[string][]byte {
	kept := make(map[string][]byte, len(files))
	for _, rel := range AgentFiles(agent) {
		if data, ok := files[rel]; ok {
			kept[rel] = data
		}
	}
	return kept
}

// claudeCredentials holds Claude's login itself.
const claudeCredentials = ".claude/.credentials.json"

// HasLogin reports whether a bundle carries a login. A logged out Claude leaves its credentials file in
// place with empty tokens, so the file being there is not enough.
func HasLogin(agent string, files map[string][]byte) bool {
	if agent != "claude" {
		return false
	}
	data, ok := files[claudeCredentials]
	if !ok {
		return false
	}
	var creds struct {
		OAuth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return false
	}
	return creds.OAuth.AccessToken != "" || creds.OAuth.RefreshToken != ""
}
