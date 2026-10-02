package credbroker

import (
	"bytes"
	"errors"
	"net"
	"testing"
)

// memStore is an in memory Store standing in for the host's opaque credential storage.
type memStore struct {
	files   map[string]map[string][]byte
	loadErr error
	saveErr error
}

func newMemStore() *memStore {
	return &memStore{files: map[string]map[string][]byte{}}
}

func (s *memStore) Load(agent string) (map[string][]byte, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.files[agent], nil
}

func (s *memStore) Save(agent string, files map[string][]byte) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.files[agent] = files
	return nil
}

// serveOne wires a guest side conn to a Claude VM's broker over an in memory pipe.
func serveOne(t *testing.T, store Store) net.Conn {
	t.Helper()
	guestConn, hostConn := net.Pipe()
	go func() { _ = Serve(hostConn, store, "claude") }()
	t.Cleanup(func() { _ = guestConn.Close() })
	return guestConn
}

func TestFetchReturnsStoredCredentials(t *testing.T) {
	store := newMemStore()
	store.files["claude"] = map[string][]byte{".claude/.credentials.json": []byte(`{"token":"secret"}`)}

	got, err := Fetch(serveOne(t, store), "claude")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got[".claude/.credentials.json"], []byte(`{"token":"secret"}`)) {
		t.Errorf("credentials = %q", got[".claude/.credentials.json"])
	}
}

// Nothing stored is the ordinary first run case: the agent has simply never logged in, and the session
// must still start so the user can do that.
func TestFetchWithNothingStoredIsNotAFailure(t *testing.T) {
	_, err := Fetch(serveOne(t, newMemStore()), "claude")
	if !errors.Is(err, ErrNoCredentials) {
		t.Errorf("error = %v, want ErrNoCredentials", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store := newMemStore()
	bundle := map[string][]byte{".claude/.credentials.json": []byte(loggedIn)}

	if err := StoreFiles(serveOne(t, store), "claude", bundle); err != nil {
		t.Fatalf("StoreFiles: %v", err)
	}
	if !bytes.Equal(store.files["claude"][".claude/.credentials.json"], bundle[".claude/.credentials.json"]) {
		t.Errorf("stored = %q", store.files["claude"][".claude/.credentials.json"])
	}

	// What went in must come back out on the next boot.
	got, err := Fetch(serveOne(t, store), "claude")
	if err != nil {
		t.Fatalf("Fetch after store: %v", err)
	}
	if !bytes.Equal(got[".claude/.credentials.json"], bundle[".claude/.credentials.json"]) {
		t.Errorf("round tripped = %q", got[".claude/.credentials.json"])
	}
}

// A guest's push is the only way into the bundle every later session gets, so nothing but the login
// files may ride along.
func TestStoreKeepsOnlyTheLoginFiles(t *testing.T) {
	store := newMemStore()
	bundle := map[string][]byte{
		".claude/.credentials.json": []byte(loggedIn),
		"../usr/local/bin/tmux":     []byte("planted"),
	}

	if err := StoreFiles(serveOne(t, store), "claude", bundle); err != nil {
		t.Fatalf("StoreFiles: %v", err)
	}
	if _, planted := store.files["claude"]["../usr/local/bin/tmux"]; planted {
		t.Error("a file outside the login list was stored")
	}
	if _, kept := store.files["claude"][".claude/.credentials.json"]; !kept {
		t.Error("the login file was dropped")
	}
}

// A Claude VM's broker answers for Claude only. Whatever else a guest names, the store is not touched.
func TestServe_RefusesWhatTheVMMayNotTouch(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		files map[string][]byte
	}{
		{name: "another agent's login", agent: "codex", files: map[string][]byte{claudeCredentials: []byte(loggedIn)}},
		{name: "the saved backup copy", agent: "claude/previous", files: map[string][]byte{claudeCredentials: []byte(loggedIn)}},
		{name: "a bundle with no login", agent: "claude", files: map[string][]byte{claudeCredentials: []byte(loggedOut)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			if err := StoreFiles(serveOne(t, store), tc.agent, tc.files); err == nil {
				t.Error("the store was accepted")
			}
			if len(store.files) != 0 {
				t.Errorf("the store was changed: %v", store.files)
			}
		})
	}
}

const (
	loggedIn  = `{"claudeAiOauth":{"accessToken":"sk-x","refreshToken":"rt-x"}}`
	loggedOut = `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0}}`
)

func TestStoreErrorSurfacesToTheGuest(t *testing.T) {
	store := newMemStore()
	store.saveErr = errors.New("disk on fire")

	err := StoreFiles(serveOne(t, store), "claude", map[string][]byte{"a": []byte("b")})
	if err == nil {
		t.Fatal("expected the store failure to reach the guest")
	}
}

func TestLoadErrorSurfacesToTheGuest(t *testing.T) {
	store := newMemStore()
	store.loadErr = errors.New("store unavailable")

	if _, err := Fetch(serveOne(t, store), "claude"); err == nil {
		t.Fatal("expected the load failure to reach the guest")
	} else if errors.Is(err, ErrNoCredentials) {
		t.Error("a store failure must not look like an empty store")
	}
}

func TestUnknownOpIsRejected(t *testing.T) {
	conn := serveOne(t, newMemStore())
	if err := writeMessage(conn, Request{Op: "delete-everything", Agent: "claude"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := readMessage[Response](conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.Error == "" {
		t.Error("an unknown op must be rejected rather than silently accepted")
	}
}
