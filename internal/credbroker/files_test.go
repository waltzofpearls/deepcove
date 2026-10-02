package credbroker

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestKeepAgentFiles(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		files map[string][]byte
		want  []string
	}{
		{
			name: "the login file kept, settings dropped", agent: "claude",
			files: map[string][]byte{".claude/.credentials.json": nil, ".claude.json": nil},
			want:  []string{".claude/.credentials.json"},
		},
		{
			name: "anything else dropped", agent: "claude",
			files: map[string][]byte{"../usr/local/bin/tmux": nil, ".claude/settings.json": nil},
		},
		{
			name: "an agent with no list keeps nothing", agent: "other",
			files: map[string][]byte{".claude.json": nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slices.Sorted(maps.Keys(KeepAgentFiles(tc.agent, tc.files)))
			if !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

// Joined onto the agent home as they are, so none may be absolute or climb out of it.
func TestAgentFiles_StayInsideTheAgentHome(t *testing.T) {
	for agent, files := range agentFiles {
		for _, rel := range files {
			if strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
				t.Errorf("%s: %q escapes the agent home", agent, rel)
			}
		}
	}
}

func TestHasLogin(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		files map[string][]byte
		want  bool
	}{
		{name: "a real login", agent: "claude", files: map[string][]byte{claudeCredentials: []byte(loggedIn)}, want: true},
		{
			name: "only a refresh token still logs in", agent: "claude",
			files: map[string][]byte{claudeCredentials: []byte(`{"claudeAiOauth":{"accessToken":"","refreshToken":"rt-x"}}`)},
			want:  true,
		},
		{name: "empty tokens are a logged out shell", agent: "claude", files: map[string][]byte{claudeCredentials: []byte(loggedOut)}},
		{name: "settings without the credentials file", agent: "claude", files: map[string][]byte{".claude.json": []byte(`{}`)}},
		{name: "unreadable json", agent: "claude", files: map[string][]byte{claudeCredentials: []byte(`not json`)}},
		{name: "another agent's name", agent: "codex", files: map[string][]byte{claudeCredentials: []byte(loggedIn)}},
		{name: "nothing at all", agent: "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasLogin(tc.agent, tc.files); got != tc.want {
				t.Errorf("HasLogin = %v, want %v", got, tc.want)
			}
		})
	}
}
