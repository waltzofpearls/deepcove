package session

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// fakeRepo is a minimal SessionRepository for the aggregation tests: only List returns data (the
// rest are unused by ListAggregated). List honors the repo contract of last-activity-desc order.
type fakeRepo struct{ sessions []*Session }

func newFakeRepoWith(sessions ...*Session) *fakeRepo { return &fakeRepo{sessions: sessions} }

func (r *fakeRepo) List(context.Context, string, bool) ([]*Session, error) {
	out := append([]*Session(nil), r.sessions...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActivityAt.After(out[j].LastActivityAt) })
	return out, nil
}
func (r *fakeRepo) Insert(context.Context, *Session) error                          { return nil }
func (r *fakeRepo) Get(context.Context, string, string) (*Session, error)           { return nil, ErrNotFound }
func (r *fakeRepo) MarkRunning(context.Context, string, string, time.Time) error    { return nil }
func (r *fakeRepo) MarkStopped(context.Context, string, string, time.Time) error    { return nil }
func (r *fakeRepo) UpdateActivity(context.Context, string, string, time.Time) error { return nil }
func (r *fakeRepo) Rename(context.Context, string, string, string, time.Time) error { return nil }
func (r *fakeRepo) UpdateUseLLMProxy(context.Context, string, string, bool, time.Time) error {
	return nil
}
func (r *fakeRepo) UpdateMountsAndSandbox(context.Context, string, string, []MountRequest, string, time.Time) error {
	return nil
}
func (r *fakeRepo) Delete(context.Context, string, string) error { return nil }

// fakeFederation is a canned Federation: it returns the configured peers, and for each peer either
// its scripted sessions or an error (to exercise the unreachable path).
type fakeFederation struct {
	self     NodeRef
	peers    []NodeRef
	results  map[string][]*Session // keyed by peer StableNodeID
	clusters map[string]string     // peer cluster label, keyed by peer StableNodeID
	errs     map[string]error      // keyed by peer StableNodeID

	createdOn string // StableNodeID of the peer CreateSession was proxied to
}

func (f *fakeFederation) Self() NodeRef    { return f.self }
func (f *fakeFederation) Peers() []NodeRef { return f.peers }
func (f *fakeFederation) FetchSessions(_ context.Context, peer NodeRef, _ bool) ([]*Session, string, error) {
	if err := f.errs[peer.StableNodeID]; err != nil {
		return nil, "", err
	}
	return f.results[peer.StableNodeID], f.clusters[peer.StableNodeID], nil
}
func (f *fakeFederation) CreateSession(_ context.Context, peer NodeRef, _ CreateRequest) (*Session, error) {
	f.createdOn = peer.StableNodeID
	return &Session{ID: "remote-created", Name: "remote-created"}, nil
}

func sessionAt(id string, activity time.Time) *Session {
	return &Session{ID: id, Name: id, LastActivityAt: activity}
}

func TestListAggregated(t *testing.T) {
	now := time.Now().UTC()
	svc := &Service{repo: newFakeRepoWith(
		sessionAt("local-old", now.Add(-2*time.Hour)),
		sessionAt("local-new", now.Add(-1*time.Minute)),
	)}

	serverPeer := NodeRef{StableNodeID: "n-server", Hostname: "deepcove-server", DNSName: "deepcove-server.ts.net"}
	cloudPeer := NodeRef{StableNodeID: "n-cloud", Hostname: "deepcove-cloud", DNSName: "deepcove-cloud.ts.net"}

	svc.SetFederation(&fakeFederation{
		self:  NodeRef{StableNodeID: "n-self", Hostname: "deepcove-laptop", Cluster: "home"},
		peers: []NodeRef{serverPeer, cloudPeer},
		results: map[string][]*Session{
			"n-server": {sessionAt("server-mid", now.Add(-30*time.Minute))},
		},
		clusters: map[string]string{
			"n-server": "prod",
		},
		errs: map[string]error{
			"n-cloud": errors.New("timeout"),
		},
	})

	resp, err := svc.ListAggregated(context.Background(), ListFilter{IncludeAll: true})
	if err != nil {
		t.Fatalf("ListAggregated: %v", err)
	}

	// Merged + sorted by last activity desc: local-new, server-mid, local-old.
	wantOrder := []string{"local-new", "server-mid", "local-old"}
	if len(resp.Sessions) != len(wantOrder) {
		t.Fatalf("got %d sessions, want %d", len(resp.Sessions), len(wantOrder))
	}
	for i, want := range wantOrder {
		if resp.Sessions[i].ID != want {
			t.Errorf("sessions[%d] = %q, want %q", i, resp.Sessions[i].ID, want)
		}
	}

	// Host tagging: local sessions -> self; the server's session -> the server peer.
	hostOf := map[string]string{}
	for _, sess := range resp.Sessions {
		if sess.Host == nil {
			t.Fatalf("session %q has no Host tag", sess.ID)
		}
		hostOf[sess.ID] = sess.Host.StableNodeID
	}
	if hostOf["local-new"] != "n-self" || hostOf["server-mid"] != "n-server" {
		t.Errorf("host tags wrong: %v", hostOf)
	}

	// Cluster tagging: local sessions carry self's cluster; the server's session carries the cluster the
	// server reported through the fan-out.
	clusterOf := map[string]string{}
	for _, sess := range resp.Sessions {
		clusterOf[sess.ID] = sess.Host.Cluster
	}
	if clusterOf["local-new"] != "home" || clusterOf["server-mid"] != "prod" {
		t.Errorf("cluster tags wrong: %v", clusterOf)
	}

	// The cloud peer errored -> reported unreachable, not dropped.
	if len(resp.Unreachable) != 1 || resp.Unreachable[0].StableNodeID != "n-cloud" {
		t.Errorf("unreachable = %v, want [n-cloud]", resp.Unreachable)
	}
	if resp.Self == nil || resp.Self.StableNodeID != "n-self" {
		t.Errorf("self = %v, want n-self", resp.Self)
	}
}

func TestCreateRouted(t *testing.T) {
	svc, _, _, _ := newTestService(t) // real repo + fakeSandbox
	self := NodeRef{StableNodeID: "n-self", Hostname: "deepcove-laptop"}
	peer := NodeRef{StableNodeID: "n-peer", Hostname: "deepcove-cloud", DNSName: "deepcove-cloud.ts.net"}
	fed := &fakeFederation{self: self, peers: []NodeRef{peer}}
	svc.SetFederation(fed)
	ctx := context.Background()

	// Empty NodeID creates locally (no proxy).
	sess, err := svc.CreateRouted(ctx, CreateRequest{Agent: "claude", Name: "local-empty"})
	if err != nil {
		t.Fatalf("empty NodeID: %v", err)
	}
	if fed.createdOn != "" || sess.ID == "remote-created" {
		t.Errorf("empty NodeID should create locally, got createdOn=%q sess=%q", fed.createdOn, sess.ID)
	}

	// This node's own id also creates locally.
	if _, err := svc.CreateRouted(ctx, CreateRequest{Agent: "claude", Name: "local-self", NodeID: "n-self"}); err != nil {
		t.Fatalf("self NodeID: %v", err)
	}
	if fed.createdOn != "" {
		t.Errorf("self NodeID should create locally, got createdOn=%q", fed.createdOn)
	}

	// A peer's id proxies the create to that peer.
	sess, err = svc.CreateRouted(ctx, CreateRequest{Agent: "claude", NodeID: "n-peer"})
	if err != nil {
		t.Fatalf("peer NodeID: %v", err)
	}
	if fed.createdOn != "n-peer" || sess.ID != "remote-created" {
		t.Errorf("peer NodeID should proxy, got createdOn=%q sess=%q", fed.createdOn, sess.ID)
	}

	// An unknown id is ErrNodeNotFound.
	if _, err := svc.CreateRouted(ctx, CreateRequest{Agent: "claude", NodeID: "nope"}); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("unknown NodeID: got %v, want ErrNodeNotFound", err)
	}
}

func TestListAggregated_SingleNode(t *testing.T) {
	// No Federation set: just this node's sessions, no Self/Unreachable, no Host tags.
	svc := &Service{repo: newFakeRepoWith(sessionAt("only", time.Now()))}
	resp, err := svc.ListAggregated(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("ListAggregated: %v", err)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].ID != "only" {
		t.Fatalf("sessions = %v", resp.Sessions)
	}
	if resp.Sessions[0].Host != nil {
		t.Errorf("single-node session should not be host-tagged, got %v", resp.Sessions[0].Host)
	}
	if resp.Self != nil || resp.Unreachable != nil {
		t.Errorf("single-node should have no Self/Unreachable, got self=%v unreachable=%v", resp.Self, resp.Unreachable)
	}
}

// A workspace session for another node is that node's business: this one must not read its own record
// or build a machine, it must hand the request over.
func TestCreateRoutedWorkspaceSessionGoesToThePeer(t *testing.T) {
	svc, lister := workspaceService(t)
	peer := NodeRef{StableNodeID: "n-peer", Hostname: "deepcove-peer"}
	fed := &fakeFederation{self: NodeRef{StableNodeID: "n-self"}, peers: []NodeRef{peer}}
	svc.SetFederation(fed)

	built := len(lister.created)
	sess, err := svc.CreateRouted(context.Background(), CreateRequest{
		Agent:         "claude",
		OnWorkspaceVM: true,
		NodeID:        peer.StableNodeID,
	})
	if err != nil {
		t.Fatalf("CreateRouted: %v", err)
	}
	if fed.createdOn != peer.StableNodeID {
		t.Errorf("created on %q, want the peer %q", fed.createdOn, peer.StableNodeID)
	}
	if sess.ID != "remote-created" {
		t.Errorf("got session %q, want the peer's", sess.ID)
	}
	if got := len(lister.created) - built; got != 0 {
		t.Errorf("built %d machines locally, want none: the peer hosts it", got)
	}
}
