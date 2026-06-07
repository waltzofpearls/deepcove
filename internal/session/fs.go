package session

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FsEntry is a single subdirectory in an FsListResponse.
type FsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// FsListResponse is the GET /api/fs/list payload — the directories the host-path
// picker/autocomplete shows. Parent is "" when Path is the home root (no "up").
type FsListResponse struct {
	Path    string    `json:"path"`
	Parent  string    `json:"parent"`
	Entries []FsEntry `json:"entries"`
}

// handleFsList lists the subdirectories of a host path so the New Session form can
// offer host-path autocomplete + a Browse modal. GET /api/fs/list?path=/abs/path
//
// Scope: the user's home directory subtree only — this mirrors the sandbox mount-source
// allowed root ($HOME) wired in start.go, so the picker can only reach folders that are
// actually mountable. Paths are cleaned (so "../" can't walk out at the string level)
// and prefix-checked against home before any read. Owner-only, local-trust model; a
// symlink inside home that points elsewhere is followed by ReadDir, which is acceptable
// here since the owner could mount such a path directly anyway.
func (s *Service) handleFsList(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		writeError(w, http.StatusInternalServerError, "cannot resolve home directory")
		return
	}
	home = filepath.Clean(home)

	path := r.URL.Query().Get("path")
	if path == "" {
		path = home
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	path = filepath.Clean(path)

	// Confine to the home subtree. Allow home itself and anything beneath it.
	if path != home && !strings.HasPrefix(path, home+string(os.PathSeparator)) {
		writeError(w, http.StatusForbidden, "path is outside the allowed home directory")
		return
	}

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "directory not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read path")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is not a directory")
		return
	}

	dirents, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusForbidden, "cannot list directory (permission denied?)")
		return
	}
	entries := make([]FsEntry, 0, len(dirents))
	for _, de := range dirents {
		if !de.IsDir() {
			continue
		}
		entries = append(entries, FsEntry{Name: de.Name(), Path: filepath.Join(path, de.Name())})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	parent := ""
	if path != home {
		parent = filepath.Dir(path)
	}
	writeJSON(w, http.StatusOK, FsListResponse{Path: path, Parent: parent, Entries: entries})
}
