package session

import (
	"encoding/json"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestCredentialStoreRoundTrip(t *testing.T) {
	store := NewCredentialStore(db.OpenForTest(t))

	if files, err := store.Load("claude"); err != nil || files != nil {
		t.Fatalf("nothing stored: got %v, %v; want nil, nil", files, err)
	}

	login := map[string][]byte{".claude/.credentials.json": []byte(`{"claudeAiOauth":{"accessToken":"sk-x"}}`)}
	if err := store.Save("claude", login); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load("claude")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(got[".claude/.credentials.json"]) != string(login[".claude/.credentials.json"]) {
		t.Errorf("got %s, want the saved bundle", got[".claude/.credentials.json"])
	}
}

// A bad push is recoverable: the bundle it replaced is kept. One guest that lost its login wiped every
// session's login, with nothing to go back to.
func TestCredentialStoreKeepsWhatItReplaced(t *testing.T) {
	database := db.OpenForTest(t)
	store := NewCredentialStore(database)

	login := map[string][]byte{".claude/.credentials.json": []byte(`{"claudeAiOauth":{"accessToken":"sk-good"}}`)}
	if err := store.Save("claude", login); err != nil {
		t.Fatalf("Save: %v", err)
	}
	emptied := map[string][]byte{".claude/.credentials.json": []byte(`{"claudeAiOauth":{"accessToken":""}}`)}
	if err := store.Save("claude", emptied); err != nil {
		t.Fatalf("Save again: %v", err)
	}

	var raw string
	if err := database.QueryRow(
		`SELECT value FROM settings WHERE key = 'agent-credentials/claude/previous'`).Scan(&raw); err != nil {
		t.Fatalf("read the previous bundle: %v", err)
	}
	var previous map[string][]byte
	if err := json.Unmarshal([]byte(raw), &previous); err != nil {
		t.Fatalf("decode the previous bundle: %v", err)
	}
	if got := string(previous[".claude/.credentials.json"]); got != string(login[".claude/.credentials.json"]) {
		t.Errorf("previous bundle %s, want the login it replaced", got)
	}
}
