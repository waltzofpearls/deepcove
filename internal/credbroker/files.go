package credbroker

import "encoding/json"

// agentFiles are the login files brokered for each agent, relative to its home. Both sides keep only
// these, so a guest cannot use the shared bundle to plant any other file in later sessions.
var agentFiles = map[string][]string{
	"claude": {claudeCredentials, claudeSettings},
}

// claudeSettings is Claude's own state: the account it is signed in as, onboarding, caches. Without it
// Claude asks to log in again even with a valid credentials file.
const claudeSettings = ".claude.json"

// claudeSettingsDenylist are the keys in Claude's settings that are never shared, since both can make
// Claude run commands: mcpServers directly, and projects through each folder's own MCP servers and
// allowed tools.
var claudeSettingsDenylist = []string{"mcpServers", "projects"}

// AgentFiles returns the login files brokered for agent, or nil for an agent with none.
func AgentFiles(agent string) []string { return agentFiles[agent] }

// KeepAgentFiles drops every file from a bundle that is not one of agent's login files, and the
// denylisted keys from Claude's settings. Settings that are not valid JSON are dropped too.
func KeepAgentFiles(agent string, files map[string][]byte) map[string][]byte {
	kept := make(map[string][]byte, len(files))
	for _, rel := range AgentFiles(agent) {
		data, ok := files[rel]
		if !ok {
			continue
		}
		if rel == claudeSettings {
			if data, ok = createSharedClaudeSettings(data); !ok {
				continue
			}
		}
		kept[rel] = data
	}
	return kept
}

// createSharedClaudeSettings copies Claude's settings without the denylisted keys.
func createSharedClaudeSettings(settings []byte) ([]byte, bool) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(settings, &keys); err != nil || keys == nil {
		return nil, false
	}
	for _, key := range claudeSettingsDenylist {
		delete(keys, key)
	}
	shared, err := json.Marshal(keys)
	return shared, err == nil
}

// MergeIntoSession is what a shared file becomes in a session that already has its own copy. Claude's
// settings keep the session's own denylisted keys; any other file is replaced outright.
func MergeIntoSession(rel string, shared, sessionCopy []byte) []byte {
	if rel != claudeSettings {
		return shared
	}
	var sharedKeys, sessionKeys map[string]json.RawMessage
	if json.Unmarshal(shared, &sharedKeys) != nil || json.Unmarshal(sessionCopy, &sessionKeys) != nil {
		return shared
	}
	for _, key := range claudeSettingsDenylist {
		if value, ok := sessionKeys[key]; ok {
			sharedKeys[key] = value
		}
	}
	merged, err := json.Marshal(sharedKeys)
	if err != nil {
		return shared
	}
	return merged
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
