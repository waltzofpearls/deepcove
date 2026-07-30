package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// uploadsDir is the in-container directory pasted files land in. Files are streamed in via
// `docker exec` (NOT a host bind mount), so nothing touches the host filesystem; agent content stays
// off the host per the user's threat model. See the paste security note in the roadmap.
const uploadsDir = "/tmp/.uploads"

// maxUploadBytes caps a single pasted file.
const maxUploadBytes = 25 << 20 // 25 MiB

// UploadResponse is the PUT /api/sessions/{id}/uploads payload: the in-container path the SPA
// injects into the agent's prompt.
type UploadResponse struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// UploadFile streams data into the running session's container at
// /tmp/.uploads/<hash>.<ext> via exec and returns the in-container path.
// No host file is written.
func (s *Service) UploadFile(ctx context.Context, id, origName string, data []byte) (*UploadResponse, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.Status != StatusRunning || sess.ContainerID == "" {
		return nil, fmt.Errorf("session is not running")
	}

	name := uploadName(data, origName)
	target := path.Join(uploadsDir, name)

	// `cat > file` reads stdin to EOF; the driver half-closes stdin
	// after copying the body, so cat exits cleanly. Inputs are a constant
	// dir + a generated name, but quote defensively anyway.
	script := fmt.Sprintf("mkdir -p %s && cat > %s", shQuote(uploadsDir), shQuote(target))
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, err
	}
	proc, err := runtime.Exec(ctx, sandbox.Handle(sess.ContainerID), sandbox.ExecSpec{
		Cmd:   []string{"sh", "-c", script},
		Stdin: bytes.NewReader(data),
		Tty:   false,
	})
	if err != nil {
		return nil, fmt.Errorf("upload: exec: %w", err)
	}
	defer func() { _ = proc.Close() }()
	if exit, werr := proc.Wait(ctx); werr != nil || exit != 0 {
		return nil, fmt.Errorf("upload: write failed (exit %d): %w", exit, werr)
	}
	return &UploadResponse{Path: target, Name: name}, nil
}

// uploadName builds a collision-resistant, shell-safe filename:
// <sha256[:16]><.ext>, ext derived from the original name (sanitized).
func uploadName(data []byte, origName string) string {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])[:16]
	return h + sanitizeExt(path.Ext(origName))
}

// sanitizeExt reduces an extension to a short lowercase alphanumeric
// suffix (with leading dot), or "" if there's nothing usable. Keeps the
// generated filename free of anything shell- or path-significant.
func sanitizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	var b strings.Builder
	for _, r := range ext {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 8 {
			break
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "." + b.String()
}

// shQuote single-quotes a string for `sh -c`.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// handleUpload streams the request body into the container and returns
// the in-container path. PUT /api/sessions/{id}/uploads?name=foo.png
func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "upload too large (max 25 MiB) or read failed")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty upload")
		return
	}
	resp, err := s.UploadFile(r.Context(), id, r.URL.Query().Get("name"), data)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
