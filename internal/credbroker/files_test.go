package credbroker

import (
	"encoding/json"
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
			name: "the login and the settings kept", agent: "claude",
			files: map[string][]byte{".claude/.credentials.json": nil, ".claude.json": []byte(`{}`)},
			want:  []string{".claude.json", ".claude/.credentials.json"},
		},
		{
			name: "settings that are not JSON dropped", agent: "claude",
			files: map[string][]byte{".claude.json": []byte("not json")},
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

func TestKeepAgentFiles_DropsTheDenylistedKeys(t *testing.T) {
	settings := []byte(`{"oauthAccount":{"emailAddress":"a@b.c"},"mcpServers":{"x":{"command":"evil"}},"projects":{"/w":{}}}`)
	kept := KeepAgentFiles("claude", map[string][]byte{".claude.json": settings})

	got := keysOf(t, kept[".claude.json"])
	if want := []string{"oauthAccount"}; !slices.Equal(got, want) {
		t.Errorf("shared keys = %v, want %v", got, want)
	}
}

func TestMergeIntoSession(t *testing.T) {
	cases := []struct {
		name        string
		rel         string
		shared      string
		sessionCopy string
		wantKeys    []string
	}{
		{"the session keeps its own MCP servers and projects", ".claude.json",
			`{"oauthAccount":{}}`, `{"oauthAccount":{"old":1},"mcpServers":{},"projects":{}}`,
			[]string{"mcpServers", "oauthAccount", "projects"}},
		{"a broken session copy is replaced", ".claude.json", `{"oauthAccount":{}}`, `broken`,
			[]string{"oauthAccount"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged := MergeIntoSession(tc.rel, []byte(tc.shared), []byte(tc.sessionCopy))
			if got := keysOf(t, merged); !slices.Equal(got, tc.wantKeys) {
				t.Errorf("keys = %v, want %v", got, tc.wantKeys)
			}
		})
	}
}

func TestMergeIntoSession_ReplacesTheCredentialsFile(t *testing.T) {
	if got := MergeIntoSession(claudeCredentials, []byte("new"), []byte("old")); string(got) != "new" {
		t.Errorf("got %q, want the shared file", got)
	}
}

func keysOf(t *testing.T, data []byte) []string {
	t.Helper()
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return slices.Sorted(maps.Keys(settings))
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
