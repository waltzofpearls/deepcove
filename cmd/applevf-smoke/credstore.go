package main

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// fileStore is a development stand in for the host's real credential storage: a single JSON file holding
// every agent's bundle. Production stores credentials opaquely inside deepcove's own data store, never at
// a credential shaped path, but for the boot harness a plain file makes the round trip observable.
type fileStore struct {
	path string
	mu   sync.Mutex
}

func newFileStore(path string) *fileStore { return &fileStore{path: path} }

func (s *fileStore) Load(agent string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	return all[agent], nil
}

func (s *fileStore) Save(agent string, files map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.readAll()
	if err != nil {
		return err
	}
	all[agent] = files
	body, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, body, 0o600)
}

// readAll returns every stored bundle. A missing file is an empty store, not an error, since nothing has
// been saved yet on a first run.
func (s *fileStore) readAll() (map[string]map[string][]byte, error) {
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string]map[string][]byte{}
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, err
	}
	return all, nil
}
