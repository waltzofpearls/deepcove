package session

import (
	"container/list"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// previewCacheMaxEntries bounds the in-memory preview cache. A reasonable single-node deepcove has
// tens of sessions at most; 256 is generous headroom. Beyond this, the least-recently-used entry is
// evicted on Set, preventing unbounded growth if session rows are removed out of band (manual SQL,
// future migrations) without going through Destroy.
const previewCacheMaxEntries = 256

// PreviewResponse is the JSON shape returned by GET /api/sessions/{id}/preview.
//
// We return the raw `tmux capture-pane -e` byte stream and let the client decide how to render it.
// Today the client feeds it into an unmounted xterm.js Terminal + SerializeAddon, which derives its
// own dimensions from the bytes, so we don't need to send cols/rows over the wire (saves a tmux exec
// per poll, simplifies the schema).
type PreviewResponse struct {
	Ansi string `json:"ansi"`
	// Stale means this could not be captured now, so it is the last picture we had, or nothing.
	Stale bool `json:"stale,omitempty"`
}

// previewCache holds the most recent successful capture per session.
// Used when the session is stopped (no live capture possible) and as
// a fallback when a live capture transiently fails (typically during
// a stop/start cycle while the container's tmux is still booting).
// Memory-only; rebuilt as live captures succeed after a service
// restart.
//
// LRU bounded to previewCacheMaxEntries: recently-accessed entries (Get OR Set) move to the front;
// cache overflow evicts the oldest. Active sessions stay warm; orphans from out-of-band row removal
// age out.
type previewCache struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element // id → list element
	order *list.List               // most-recent at front
}

type previewEntry struct {
	id      string
	preview *PreviewResponse
}

func newPreviewCache(max int) *previewCache {
	return &previewCache{
		max:   max,
		items: make(map[string]*list.Element),
		order: list.New(),
	}
}

func (c *previewCache) Get(id string) *PreviewResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[id]
	if !ok {
		return nil
	}
	c.order.MoveToFront(e)
	return e.Value.(*previewEntry).preview
}

func (c *previewCache) Set(id string, p *PreviewResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[id]; ok {
		e.Value.(*previewEntry).preview = p
		c.order.MoveToFront(e)
		return
	}
	e := c.order.PushFront(&previewEntry{id: id, preview: p})
	c.items[id] = e
	if c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*previewEntry).id)
		}
	}
}

func (c *previewCache) Delete(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[id]; ok {
		c.order.Remove(e)
		delete(c.items, id)
	}
}

func (s *Service) Preview(ctx context.Context, id string) (*PreviewResponse, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if sess.Status != StatusRunning {
		if cached := s.previewCache.Get(id); cached != nil {
			return cached, nil
		}
		return &PreviewResponse{}, nil
	}

	resp := s.fetchLivePreview(ctx, sess)
	if resp != nil {
		s.previewCache.Set(id, resp)
		return resp, nil
	}

	// The capture failed, so whatever we hand back is old. A copy, because the cache keeps the original.
	if cached := s.previewCache.Get(id); cached != nil {
		old := *cached
		old.Stale = true
		return &old, nil
	}
	return &PreviewResponse{Stale: true}, nil
}

// fetchLivePreview execs `tmux capture-pane -p -e -t main` and returns
// the captured bytes. Returns nil on any failure so the caller can
// fall back to cache.
func (s *Service) fetchLivePreview(ctx context.Context, sess *Session) *PreviewResponse {
	execCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ansi, ok := s.execCapture(execCtx, sess,
		[]string{"tmux", "capture-pane", "-p", "-e", "-t", tmuxSessionName(sess.ID)})
	if !ok || len(ansi) == 0 {
		return nil
	}
	return &PreviewResponse{Ansi: string(ansi)}
}

func (s *Service) execCapture(ctx context.Context, sess *Session, cmd []string) ([]byte, bool) {
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, false
	}
	proc, err := runtime.Exec(ctx, sandbox.ID(sess.SandboxID), sandbox.ExecSpec{
		Cmd: cmd,
		Tty: false,
	})
	if err != nil {
		s.logger.Debug("preview: exec failed", "sandbox", sess.SandboxID, "cmd", cmd[0], "err", err)
		return nil, false
	}
	defer func() { _ = proc.Close() }()

	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		s.logger.Debug("preview: stdout read failed", "sandbox", sess.SandboxID, "err", err)
		return nil, false
	}
	if exit, werr := proc.Wait(ctx); werr != nil || exit != 0 {
		s.logger.Debug("preview: non-zero exit",
			"sandbox", sess.SandboxID, "cmd", cmd[0], "exit", exit, "err", werr)
		return nil, false
	}
	return out, true
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.Preview(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeNotHostedHere(w)
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
