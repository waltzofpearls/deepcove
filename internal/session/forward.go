package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
)

// A session lives on one node. When a request lands on a node that does not have it, we find the node
// that does and pass the request along.

// forwardedHeader marks a request another node already passed along. It stops two nodes bouncing the
// same request back and forth.
const forwardedHeader = "Deepcove-Forwarded"

// nodeHeader names the node a request is for. Set by the browser when it wants another node.
const nodeHeader = "Deepcove-Node"

// forwardRetries is how many times a wrong guess may be corrected before we give up.
const forwardRetries = 1

// maxKeptRetryBodySize is how much of a request body is held for a possible retry. A JSON write is far
// under it; a file upload is far over, and is sent once rather than buffered.
const maxKeptRetryBodySize = 1 << 20

// forwardRequest is one attempt to answer a request from another node.
type forwardRequest struct {
	id      string
	node    NodeRef
	retries int
	// body is the request body, kept so a retry can send it again. Nil means there was none, or it was
	// too large to keep.
	body []byte
	// hasBody records that the request had one, so a retry that cannot resend it is refused rather than
	// sent empty.
	hasBody bool
}

// errWrongHost means the node we asked does not have the session after all.
var errWrongHost = errors.New("session: forwarded to the wrong node")

// forwardOrServe serves the request here, or sends it to the node that should answer it.
//
// The node is named by the header, or by the session in the path. One wrapper covers every route in
// this package, including ones added later.
func (s *Service) forwardOrServe(inner *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No other nodes, or another node sent this here, so we are the end of the line.
		if s.federation == nil || r.Header.Get(forwardedHeader) != "" {
			inner.ServeHTTP(w, r)
			return
		}
		node, away, err := s.targetNode(inner, r)
		switch {
		case errors.Is(err, ErrNodeNotFound):
			writeErrorCode(w, http.StatusNotFound, codeNotFound, "no such node")
		case errors.Is(err, ErrNotFound):
			writeErrorCode(w, http.StatusNotFound, codeNotFound, "session not found")
		case err != nil:
			// The lookup failed, which is not an answer about where the session lives. Saying "not found"
			// here would send the caller looking for a session that may well exist.
			s.logger.Warn("could not work out which node has a session", "err", err)
			writeErrorCode(w, http.StatusInternalServerError, codeInternal, "could not look up the session")
		case away:
			s.proxyTo(w, r, newForwardRequest(sessionID(inner, r), node, r))
		default:
			inner.ServeHTTP(w, r)
		}
	})
}

// targetNode is the node that should answer. away is false when that is us.
func (s *Service) targetNode(inner *http.ServeMux, r *http.Request) (node NodeRef, away bool, err error) {
	if id := r.Header.Get(nodeHeader); id != "" {
		if id == s.federation.Self().StableNodeID {
			return NodeRef{}, false, nil
		}
		for _, peer := range s.federation.Peers() {
			if peer.StableNodeID == id {
				return peer, true, nil
			}
		}
		return NodeRef{}, false, ErrNodeNotFound
	}
	id := sessionID(inner, r)
	mine, err := s.hostsLocally(r.Context(), id)
	if err != nil {
		return NodeRef{}, false, err
	}
	if mine {
		return NodeRef{}, false, nil
	}
	found, err := s.resolveHost(r.Context(), id)
	if err != nil {
		return NodeRef{}, false, err
	}
	return found, true, nil
}

// sessionID returns the session in the path, asking the mux which route matched. A literal route like
// /api/sessions/suggest-name is never mistaken for an id, because its pattern has no {id}.
func sessionID(inner *http.ServeMux, r *http.Request) string {
	_, pattern := inner.Handler(r)
	if !strings.Contains(pattern, "{id}") {
		return ""
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/api/sessions/")
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// hostsLocally reports whether this node has the session, straight from the database.
//
// An error is returned rather than folded into "no": a database that cannot be read says nothing about
// where the session lives, and treating it as an answer forwards the request to a peer that has never
// heard of it.
func (s *Service) hostsLocally(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return true, nil // not a session route after all; let the handler answer.
	}
	switch _, err := s.repo.Get(ctx, placeholderOwner, id); {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// resolveHost finds the node hosting a session: gossip first, then ask every node.
func (s *Service) resolveHost(ctx context.Context, id string) (NodeRef, error) {
	if node, ok := s.federation.FindSessionHost(ctx, id); ok {
		return node, nil
	}
	return s.askPeers(ctx, id)
}

// askPeers asks every node at once and takes the first yes.
func (s *Service) askPeers(ctx context.Context, id string) (NodeRef, error) {
	peers := s.federation.Peers()
	if len(peers) == 0 {
		return NodeRef{}, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, peerFanoutTimeout)
	defer cancel()

	type answer struct {
		node NodeRef
		yes  bool
	}
	answers := make(chan answer, len(peers))
	for _, peer := range peers {
		go func(p NodeRef) { answers <- answer{p, s.peerHasSession(ctx, p, id)} }(peer)
	}
	for range peers {
		if a := <-answers; a.yes {
			return a.node, nil
		}
	}
	return NodeRef{}, ErrNotFound
}

// peerHasSession asks one node whether it has the session. The forwarded header keeps it from passing
// the question on to someone else.
func (s *Service) peerHasSession(ctx context.Context, peer NodeRef, id string) bool {
	client := s.federation.TailnetClient()
	if peer.DNSName == "" || client == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+peer.DNSName+"/api/sessions/"+id, nil)
	if err != nil {
		return false
	}
	req.Header.Set(forwardedHeader, "1")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// proxyTo passes the request to another node and streams the answer back. One proxy covers every
// session route, including the terminal websocket.
func (s *Service) proxyTo(w http.ResponseWriter, r *http.Request, f forwardRequest) {
	client := s.federation.TailnetClient()
	if client == nil {
		writeErrorCode(w, http.StatusBadGateway, codeNodeUnreachable, "this node is not on your Tailscale network")
		return
	}
	proxy := &httputil.ReverseProxy{
		Transport: client.Transport,
		Rewrite:   func(pr *httputil.ProxyRequest) { rewriteToNode(pr, f.node) },
		ModifyResponse: func(resp *http.Response) error {
			if disowned(resp) {
				return errWrongHost
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			s.handleForwardError(w, req, f, err)
		},
	}
	proxy.ServeHTTP(w, r)
}

// newForwardRequest prepares a request for forwarding, keeping a small body so a wrong guess can be
// corrected. ReverseProxy reads the body once, so a retry without this reaches the right node empty.
func newForwardRequest(id string, node NodeRef, r *http.Request) forwardRequest {
	f := forwardRequest{id: id, node: node, retries: forwardRetries, hasBody: r.ContentLength != 0}
	if r.Body == nil || r.ContentLength <= 0 || r.ContentLength > maxKeptRetryBodySize {
		return f
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return f
	}
	f.body = body
	r.Body = io.NopCloser(bytes.NewReader(body))
	return f
}

// rewriteToNode points a forwarded request at the node that owns the session.
//
// The Origin header goes, because this hop is a Go client rather than a browser. Leaving the calling
// page's Origin on a request whose Host we just rewrote makes the two disagree, and the receiving node
// refuses the terminal's WebSocket over it.
func rewriteToNode(pr *httputil.ProxyRequest, node NodeRef) {
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = node.DNSName
	pr.Out.Host = node.DNSName
	pr.Out.Header.Set(forwardedHeader, "1")
	pr.Out.Header.Del("Origin")
}

// handleForwardError sorts a wrong guess from a node that is down. A wrong guess is retried once, a
// node that is down is reported as itself.
func (s *Service) handleForwardError(w http.ResponseWriter, r *http.Request, f forwardRequest, err error) {
	if !errors.Is(err, errWrongHost) {
		s.logger.Warn("could not reach the node hosting a session",
			"id", f.id, "node", nodeLabel(f.node), "err", err)
		writeErrorCode(w, http.StatusBadGateway, codeNodeUnreachable,
			fmt.Sprintf("node %s is unreachable", nodeLabel(f.node)))
		return
	}
	// Gossip named this node and it disowned the session, so ask everyone rather than believe the same
	// answer twice.
	if f.retries > 0 && f.canResend() {
		if owner, rerr := s.askPeers(r.Context(), f.id); rerr == nil {
			retry := f
			retry.node = owner
			retry.retries--
			if retry.body != nil {
				r.Body = io.NopCloser(bytes.NewReader(retry.body))
			}
			s.proxyTo(w, r, retry)
			return
		}
	}
	writeErrorCode(w, http.StatusNotFound, codeNotFound, "session not found")
}

// canResend reports whether this request can be sent a second time: it either had no body, or its body
// was small enough to keep.
func (f forwardRequest) canResend() bool { return !f.hasBody || f.body != nil }

// disowned reports whether the node says it does not have the session. The body is put back, so any
// other 404 passes through untouched.
func disowned(resp *http.Response) bool {
	if resp.StatusCode != http.StatusNotFound {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Code == codeNotHostedHere
}

// nodeLabel is the friendliest name a node has.
func nodeLabel(node NodeRef) string {
	if node.Hostname != "" {
		return node.Hostname
	}
	if node.DNSName != "" {
		return node.DNSName
	}
	return node.StableNodeID
}
