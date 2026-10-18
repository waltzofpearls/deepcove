package session

import (
	"cmp"
	"context"
	"github.com/waltzofpearls/deepcove/internal/db"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

// peerNode turns a test server into a node the forwarder can dial.
func peerNode(t *testing.T, server *httptest.Server, name string) NodeRef {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return NodeRef{StableNodeID: name, Hostname: name, DNSName: parsed.Host}
}

func TestDisowned(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"says not hosted here", 404, `{"error":"session not found","code":"not_hosted_here"}`, true},
		{"a different 404 passes through", 404, `{"error":"no such window","code":"not_found"}`, false},
		{"success is never disowned", 200, `{"id":"abc"}`, false},
		{"unparseable body is not disowned", 404, `oops`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			if got := disowned(resp); got != tc.want {
				t.Errorf("disowned = %v, want %v", got, tc.want)
			}
			// The body must survive, or a passed-through 404 would reach the browser empty.
			rest, _ := io.ReadAll(resp.Body)
			if string(rest) != tc.body {
				t.Errorf("body = %q, want it put back as %q", rest, tc.body)
			}
		})
	}
}

func TestNodeLabel(t *testing.T) {
	cases := []struct {
		name string
		node NodeRef
		want string
	}{
		{"hostname wins", NodeRef{Hostname: "droplet", DNSName: "d.ts.net", StableNodeID: "n1"}, "droplet"},
		{"falls back to dns", NodeRef{DNSName: "d.ts.net", StableNodeID: "n1"}, "d.ts.net"},
		{"then the id", NodeRef{StableNodeID: "n1"}, "n1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeLabel(tc.node); got != tc.want {
				t.Errorf("nodeLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPeerHasSession(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"peer has it", 200, `{"id":"s1"}`, true},
		{"peer does not", 404, `{"error":"session not found","code":"not_hosted_here"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sawHeader string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawHeader = r.Header.Get(forwardedHeader)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)

			svc := &Service{}
			svc.SetFederation(&fakeFederation{client: server.Client()})
			got := svc.peerHasSession(context.Background(), peerNode(t, server, "n1"), "s1")
			if got != tc.want {
				t.Errorf("peerHasSession = %v, want %v", got, tc.want)
			}
			// Without this the peer would ask its own peers, and two nodes could loop.
			if sawHeader == "" {
				t.Error("the forwarded header was not sent")
			}
		})
	}
}

func TestAskPeersTakesTheFirstYes(t *testing.T) {
	no := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_hosted_here"}`))
	}))
	t.Cleanup(no.Close)
	yes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	}))
	t.Cleanup(yes.Close)

	svc := &Service{}
	svc.SetFederation(&fakeFederation{
		client: yes.Client(),
		peers:  []NodeRef{peerNode(t, no, "no-node"), peerNode(t, yes, "yes-node")},
	})

	node, err := svc.askPeers(context.Background(), "s1")
	if err != nil {
		t.Fatalf("askPeers: %v", err)
	}
	if node.StableNodeID != "yes-node" {
		t.Errorf("found %q, want yes-node", node.StableNodeID)
	}
}

func TestAskPeersWithNobodyHoldingIt(t *testing.T) {
	no := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_hosted_here"}`))
	}))
	t.Cleanup(no.Close)

	svc := &Service{}
	svc.SetFederation(&fakeFederation{client: no.Client(), peers: []NodeRef{peerNode(t, no, "n1")}})

	if _, err := svc.askPeers(context.Background(), "gone"); err == nil {
		t.Fatal("wanted an error when no node has the session")
	}
}

// The wrapper sits on /api/, so these two assumptions decide whether every other route still works.
func TestRoutingThroughTheWrapper(t *testing.T) {
	svc := &Service{}
	inner := http.NewServeMux()
	var gotID string
	inner.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		gotID = r.PathValue("id") // set by the inner mux, even though the outer one matched "/api/"
		w.WriteHeader(http.StatusOK)
	})
	inner.HandleFunc("GET /api/sessions/local", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("local list"))
	})

	outer := http.NewServeMux()
	outer.Handle("/api/", svc.forwardOrServe(inner))
	// Another package's route must still win over the prefix.
	outer.HandleFunc("POST /api/connect", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("connect"))
	})

	cases := []struct {
		name, method, path, wantBody string
		wantID                       string
	}{
		{name: "a session route fills PathValue", method: "GET", path: "/api/sessions/abc123", wantID: "abc123"},
		{name: "a literal route is not an id", method: "GET", path: "/api/sessions/local", wantBody: "local list"},
		{name: "a more specific route wins", method: "POST", path: "/api/connect", wantBody: "connect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID = ""
			rec := httptest.NewRecorder()
			outer.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if tc.wantID != "" && gotID != tc.wantID {
				t.Errorf("id = %q, want %q", gotID, tc.wantID)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestSessionIDUsesTheMatchedRoute(t *testing.T) {
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/sessions/{id}", func(http.ResponseWriter, *http.Request) {})
	inner.HandleFunc("GET /api/sessions/{id}/preview", func(http.ResponseWriter, *http.Request) {})
	inner.HandleFunc("GET /api/sessions/local", func(http.ResponseWriter, *http.Request) {})
	inner.HandleFunc("GET /api/preflight", func(http.ResponseWriter, *http.Request) {})

	cases := []struct{ path, want string }{
		{"/api/sessions/abc123", "abc123"},
		{"/api/sessions/abc123/preview", "abc123"},
		{"/api/sessions/local", ""}, // a literal route, not a session
		{"/api/preflight", ""},      // node-scoped, no session at all
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := sessionID(inner, httptest.NewRequest("GET", tc.path, nil))
			if got != tc.want {
				t.Errorf("sessionID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveHostPrefersGossip(t *testing.T) {
	// A peer that would answer "yes" if asked, so a fan-out is visible when it happens.
	asked := 0
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	}))
	t.Cleanup(peer.Close)

	gossiped := NodeRef{StableNodeID: "from-gossip", Hostname: "droplet"}
	svc := &Service{}
	svc.SetFederation(&fakeFederation{
		client: peer.Client(),
		peers:  []NodeRef{peerNode(t, peer, "from-fanout")},
		hosts:  map[string]NodeRef{"s1": gossiped},
	})

	node, err := svc.resolveHost(context.Background(), "s1")
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if node.StableNodeID != "from-gossip" {
		t.Errorf("resolved to %q, want the node gossip named", node.StableNodeID)
	}
	if asked != 0 {
		t.Errorf("asked %d peers, want none when gossip already knows", asked)
	}
}

func TestResolveHostFallsBackToAsking(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	}))
	t.Cleanup(peer.Close)

	// Nothing in gossip yet, which is the case for a session created moments ago.
	svc := &Service{}
	svc.SetFederation(&fakeFederation{
		client: peer.Client(),
		peers:  []NodeRef{peerNode(t, peer, "from-fanout")},
	})

	node, err := svc.resolveHost(context.Background(), "s1")
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if node.StableNodeID != "from-fanout" {
		t.Errorf("resolved to %q, want the node that answered", node.StableNodeID)
	}
}

func TestRewriteToNode(t *testing.T) {
	in := httptest.NewRequest("GET", "http://127.0.0.1:9000/api/sessions/abc/attach", nil)
	in.Header.Set("Origin", "http://127.0.0.1:9000")
	in.Header.Set("Sec-WebSocket-Key", "kept")
	pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}

	rewriteToNode(pr, NodeRef{DNSName: "peer.tail9c1f4.ts.net"})

	if pr.Out.Host != "peer.tail9c1f4.ts.net" || pr.Out.URL.Host != "peer.tail9c1f4.ts.net" {
		t.Errorf("host = %q / %q, want the peer", pr.Out.Host, pr.Out.URL.Host)
	}
	if got := pr.Out.Header.Get("Origin"); got != "" {
		t.Errorf("Origin = %q, want it dropped so the peer's websocket check passes", got)
	}
	if pr.Out.Header.Get(forwardedHeader) != "1" {
		t.Error("the forwarded marker must survive")
	}
	if pr.Out.Header.Get("Sec-WebSocket-Key") != "kept" {
		t.Error("other headers must be left alone")
	}
}

func TestNewForwardRequestKeepsASmallBody(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		length   int64
		wantKept bool
		wantSend bool
	}{
		{name: "no body at all", wantSend: true},
		{name: "a json write is kept", body: `{"name":"x"}`, wantKept: true, wantSend: true},
		{name: "an upload is too large to keep", body: "x", length: maxKeptRetryBodySize + 1},
		{name: "an unknown length is not kept", body: "x", length: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", "/api/sessions/abc/shared-folders", strings.NewReader(tc.body))
			r.ContentLength = cmp.Or(tc.length, int64(len(tc.body)))
			f := newForwardRequest("abc", NodeRef{DNSName: "peer"}, r)

			if (f.body != nil) != tc.wantKept {
				t.Errorf("kept = %v, want %v", f.body != nil, tc.wantKept)
			}
			if f.canResend() != tc.wantSend {
				t.Errorf("canResend = %v, want %v", f.canResend(), tc.wantSend)
			}
			if tc.wantKept {
				again, _ := io.ReadAll(r.Body)
				if string(again) != tc.body {
					t.Errorf("the request body must still be readable, got %q", again)
				}
			}
		})
	}
}

func TestHostsLocallyTellsMissingFromBroken(t *testing.T) {
	database := db.OpenForTest(t)
	svc := newServiceWithRuntime(t, database, newFakeSandbox())
	sess := mustCreate(t, svc, "claude")
	ctx := context.Background()

	if mine, err := svc.hostsLocally(ctx, sess.ID); err != nil || !mine {
		t.Errorf("its own session: got (%v, %v), want (true, nil)", mine, err)
	}
	if mine, err := svc.hostsLocally(ctx, "01a0-not-here"); err != nil || mine {
		t.Errorf("a session it does not have: got (%v, %v), want (false, nil)", mine, err)
	}

	_ = database.Close()
	if _, err := svc.hostsLocally(ctx, sess.ID); err == nil {
		t.Error("a database it cannot read must be an error, not an answer")
	}
}
