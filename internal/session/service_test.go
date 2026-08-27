package session

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// newTestService spins up a Service backed by an in-memory DuckDB
// session repo and a fake sandbox. Returns the *sql.DB too so tests
// that need to inspect the underlying table (e.g. rowCount) can do
// so directly; that's a test-only escape hatch, not a public API.
func newTestService(t *testing.T) (*Service, *fakeSandbox, string, *sql.DB) {
	t.Helper()
	database := db.OpenForTest(t)
	fake := newFakeSandbox()
	dataDir := t.TempDir()
	svc := newServiceWithRuntime(t, database, fake)
	return svc, fake, dataDir, database
}

// newServiceWithRuntime wires a service with one registered runtime, standing in for what the CLI does
// at startup now that the service no longer assumes a runtime of its own.
func newServiceWithRuntime(t *testing.T, database *sql.DB, sb sandbox.Sandbox) *Service {
	t.Helper()
	return newServiceWithNamedRuntime(t, database, sb, RuntimeDocker)
}

// newServiceWithNamedRuntime registers the fake under a chosen runtime name. Workspace VM tests need a
// VM runtime, because a workspace VM is refused on anything else.
func newServiceWithNamedRuntime(t *testing.T, database *sql.DB, sb sandbox.Sandbox, name string) *Service {
	t.Helper()
	svc := NewService(NewDuckDBSessionRepository(database), nil)
	svc.SetWorkspaceVMStore(NewDuckDBWorkspaceVMStore(database))
	// Background work is dropped: a test asserting an exact driver call sequence cannot race a goroutine
	// making its own exec calls. Tests that want it call the function directly.
	svc.background = func(func()) {}
	svc.RegisterRuntime(name, sb)
	// SetDefaultRuntime, not SelectDefaultRuntime: the latter probes for a reachable runtime, and a test
	// asserting an exact driver call sequence should not carry that probe.
	if err := svc.SetDefaultRuntime(name); err != nil {
		t.Fatalf("SetDefaultRuntime: %v", err)
	}
	return svc
}

// mustCreate is a setup helper for tests that need a pre-existing
// session. Failures here are setup failures, not the thing under
// test, so they bubble up as t.Fatalf.
func mustCreate(t *testing.T, svc *Service, agent string) *Session {
	t.Helper()
	sess, err := svc.Create(context.Background(), CreateRequest{Agent: agent})
	if err != nil {
		t.Fatalf("setup Create(%q): %v", agent, err)
	}
	return sess
}

// assertSessionErr is the local equivalent of the repository test
// helper, kept per-package because each package has slightly
// different error sentinels.
func assertSessionErr(t *testing.T, got, wantIs error, wantContains string) {
	t.Helper()
	switch {
	case wantIs != nil:
		if !errors.Is(got, wantIs) {
			t.Errorf("want errors.Is(err, %v), got %v", wantIs, got)
		}
	case wantContains != "":
		if got == nil || !strings.Contains(got.Error(), wantContains) {
			t.Errorf("want err containing %q, got %v", wantContains, got)
		}
	default:
		if got != nil {
			t.Errorf("want nil error, got %v", got)
		}
	}
}

// rowCount counts rows in the sessions table; used by several tables
// to verify create/rollback effects on persistence.
func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

func TestService_Create(t *testing.T) {
	tests := []struct {
		name             string
		req              CreateRequest
		startErr         error  // injected on the fake before Create
		createErr        error  // injected on the fake before Create
		wantErrContains  string // substring match; empty = expect success
		wantCallSequence string // expected sandbox method order; empty = don't check
		wantLive         int    // sandboxes still alive after Create
		wantRows         int    // sessions rows after Create
		wantStatus       Status // checked only if wantRows > 0
	}{
		{
			name:             "happy path with known agent",
			req:              CreateRequest{Agent: "claude", Env: map[string]string{"FOO": "bar"}},
			wantCallSequence: "Ping,Create,Start",
			wantLive:         1,
			wantRows:         1,
			wantStatus:       StatusRunning,
		},
		{
			// In v1 the registry only contains Claude; replace this with a second known
			// agent (e.g. codex) once it's added to internal/session/agents.go. Kept as a
			// placeholder so the diff is small when that happens.
			name:             "happy path with explicit name override",
			req:              CreateRequest{Agent: "claude", Name: "explicit-name"},
			wantCallSequence: "Ping,Create,Start",
			wantLive:         1,
			wantRows:         1,
			wantStatus:       StatusRunning,
		},
		{
			name:            "missing agent name rejects before sandbox",
			req:             CreateRequest{Agent: ""},
			wantErrContains: "agent is required",
			wantLive:        0,
			wantRows:        0,
		},
		{
			name:            "unknown agent rejects before sandbox",
			req:             CreateRequest{Agent: "bogus"},
			wantErrContains: "unknown agent",
			wantLive:        0,
			wantRows:        0,
		},
		{
			name:             "sandbox start failure rolls back everything",
			req:              CreateRequest{Agent: "claude"},
			startErr:         errors.New("boom"),
			wantErrContains:  "boom",
			wantCallSequence: "Ping,Create,Start,Destroy",
			wantLive:         0,
			wantRows:         0,
		},
		{
			name:             "sandbox create failure stops before persist",
			req:              CreateRequest{Agent: "claude"},
			createErr:        errors.New("daemon-down"),
			wantErrContains:  "daemon-down",
			wantCallSequence: "Ping,Create",
			wantLive:         0,
			wantRows:         0,
		},
		{
			// Mount validation rejects before any sandbox call; the
			// empty callSequence assertion below confirms we don't
			// spin up the workspace dir for a request we already
			// know is bad.
			name: "invalid mount rejects before sandbox + persist",
			req: CreateRequest{
				Agent: "claude",
				Mounts: []MountRequest{
					{HostPath: "/tmp/x", ContainerPath: "/root"},
				},
			},
			wantErrContains:  "reserved for the agent home",
			wantCallSequence: "",
			wantLive:         0,
			wantRows:         0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, db := newTestService(t)
			fake.startErr = tc.startErr
			fake.createErr = tc.createErr

			sess, err := svc.Create(context.Background(), tc.req)
			assertSessionErr(t, err, nil, tc.wantErrContains)

			if tc.wantCallSequence != "" {
				if got := fake.callSequence(); got != tc.wantCallSequence {
					t.Errorf("sandbox call sequence: want %q, got %q", tc.wantCallSequence, got)
				}
			}
			if fake.liveCount() != tc.wantLive {
				t.Errorf("live sandboxes: want %d, got %d", tc.wantLive, fake.liveCount())
			}
			if got := rowCount(t, db); got != tc.wantRows {
				t.Errorf("sessions rows: want %d, got %d", tc.wantRows, got)
			}

			// On success the session should be running and named (auto or explicit).
			// There's no host-side scratch dir anymore; /workspace lives inside the
			// container (or is a user mount), so there's nothing on disk to assert.
			if tc.wantRows > 0 && sess != nil {
				if sess.Status != tc.wantStatus {
					t.Errorf("status: want %q, got %q", tc.wantStatus, sess.Status)
				}
				if sess.Name == "" {
					t.Errorf("name not populated")
				}
				// Auto-name should start with `<agent>-` only when the request didn't
				// supply an explicit name. Explicit names should pass through verbatim.
				if tc.req.Name == "" && !strings.HasPrefix(sess.Name, tc.req.Agent+"-") {
					t.Errorf("auto-name should start with agent prefix; got %q", sess.Name)
				}
				if tc.req.Name != "" && sess.Name != tc.req.Name {
					t.Errorf("explicit name not preserved: want %q, got %q", tc.req.Name, sess.Name)
				}
			} else if sess != nil {
				t.Errorf("expected nil session on failure, got %+v", sess)
			}
		})
	}
}

func TestService_List(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, svc *Service) // creates / stops sessions to set up state
		filter     ListFilter
		wantCount  int
		wantStatus Status // expected status of all returned sessions (only checked when wantCount == 1)
	}{
		{
			name:      "empty DB returns empty list",
			setup:     func(t *testing.T, svc *Service) {},
			filter:    ListFilter{},
			wantCount: 0,
		},
		{
			name: "default filter returns only running",
			setup: func(t *testing.T, svc *Service) {
				// Two claude sessions with explicit names so the UNIQUE constraint on
				// `name` doesn't bite; in v1 the registry only has one agent, so we
				// can't differentiate by agent name like the test used to.
				_, _ = svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "list-running"})
				stopped, _ := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "list-stopped"})
				if err := svc.Stop(context.Background(), stopped.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
			},
			filter:     ListFilter{},
			wantCount:  1,
			wantStatus: StatusRunning,
		},
		{
			name: "IncludeAll returns running and stopped",
			setup: func(t *testing.T, svc *Service) {
				_, _ = svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "list-all-1"})
				stopped, _ := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "list-all-2"})
				if err := svc.Stop(context.Background(), stopped.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
			},
			filter:    ListFilter{IncludeAll: true},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			tc.setup(t, svc)

			got, err := svc.List(context.Background(), tc.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("List size: want %d, got %d (%v)", tc.wantCount, len(got), got)
			}
			if tc.wantCount == 1 && got[0].Status != tc.wantStatus {
				t.Errorf("status: want %q, got %q", tc.wantStatus, got[0].Status)
			}
		})
	}
}

func TestService_Get(t *testing.T) {
	tests := []struct {
		name       string
		setupID    func(t *testing.T, svc *Service) string // returns the id to Get
		wantErrIs  error
		wantStatus Status
	}{
		{
			name: "existing session is returned",
			setupID: func(t *testing.T, svc *Service) string {
				return mustCreate(t, svc, "claude").ID
			},
			wantStatus: StatusRunning,
		},
		{
			name: "unknown id returns ErrNotFound",
			setupID: func(t *testing.T, svc *Service) string {
				return "no-such-id"
			},
			wantErrIs: ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			id := tc.setupID(t, svc)

			sess, err := svc.Get(context.Background(), id)
			assertSessionErr(t, err, tc.wantErrIs, "")

			if tc.wantErrIs == nil {
				if sess == nil {
					t.Fatal("got nil session on success")
				}
				if sess.Status != tc.wantStatus {
					t.Errorf("status: want %q, got %q", tc.wantStatus, sess.Status)
				}
			}
		})
	}
}

func TestService_Stop(t *testing.T) {
	tests := []struct {
		name            string
		setupID         func(t *testing.T, svc *Service) string
		wantErrIs       error
		wantStatusAfter Status // only checked if wantErrIs == nil
	}{
		{
			name: "running session becomes stopped",
			setupID: func(t *testing.T, svc *Service) string {
				return mustCreate(t, svc, "claude").ID
			},
			wantStatusAfter: StatusStopped,
		},
		{
			name: "already-stopped session is a no-op",
			setupID: func(t *testing.T, svc *Service) string {
				sess := mustCreate(t, svc, "claude")
				if err := svc.Stop(context.Background(), sess.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
				return sess.ID
			},
			wantStatusAfter: StatusStopped,
		},
		{
			name: "unknown id returns ErrNotFound",
			setupID: func(t *testing.T, svc *Service) string {
				return "no-such-id"
			},
			wantErrIs: ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			id := tc.setupID(t, svc)

			gotErr := svc.Stop(context.Background(), id)
			assertSessionErr(t, gotErr, tc.wantErrIs, "")

			if tc.wantErrIs == nil {
				sess, err := svc.Get(context.Background(), id)
				if err != nil {
					t.Fatalf("Get after Stop: %v", err)
				}
				if sess.Status != tc.wantStatusAfter {
					t.Errorf("status after Stop: want %q, got %q", tc.wantStatusAfter, sess.Status)
				}
			}
		})
	}
}

func TestService_Destroy(t *testing.T) {
	tests := []struct {
		name          string
		setupID       func(t *testing.T, svc *Service) string
		wantErrIs     error
		wantWorkspace bool // true = workspace should remain after Destroy (e.g. setup didn't create one)
		wantRowsAfter int
		wantLiveAfter int
	}{
		{
			name: "existing session is fully removed",
			setupID: func(t *testing.T, svc *Service) string {
				return mustCreate(t, svc, "claude").ID
			},
			wantWorkspace: false,
			wantRowsAfter: 0,
			wantLiveAfter: 0,
		},
		{
			name: "unknown id returns ErrNotFound",
			setupID: func(t *testing.T, svc *Service) string {
				return "no-such-id"
			},
			wantErrIs:     ErrNotFound,
			wantRowsAfter: 0,
			wantLiveAfter: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, dataDir, db := newTestService(t)
			id := tc.setupID(t, svc)

			gotErr := svc.Destroy(context.Background(), id)
			assertSessionErr(t, gotErr, tc.wantErrIs, "")

			if got := rowCount(t, db); got != tc.wantRowsAfter {
				t.Errorf("row count after Destroy: want %d, got %d", tc.wantRowsAfter, got)
			}
			if fake.liveCount() != tc.wantLiveAfter {
				t.Errorf("live sandboxes after Destroy: want %d, got %d", tc.wantLiveAfter, fake.liveCount())
			}
			workspace := filepath.Join(dataDir, "sessions", id)
			if pathExists(workspace) != tc.wantWorkspace {
				t.Errorf("workspace existence: want %v, got %v", tc.wantWorkspace, pathExists(workspace))
			}
		})
	}
}

func TestAgents_ClaudeRegistryDefaults(t *testing.T) {
	// Pins the Claude registry entry's load-bearing fields so accidental edits during
	// refactors are caught before they ship. If we deliberately update the install
	// command or launch loop, this test gets updated alongside.
	a, err := resolveAgent("claude")
	if err != nil {
		t.Fatalf("resolveAgent(claude): %v", err)
	}
	if a.Name != "claude" {
		t.Errorf("Name: want claude, got %q", a.Name)
	}
	if a.DisplayName == "" {
		t.Errorf("DisplayName must be non-empty (UI displays it in the dropdown)")
	}
	if a.DefaultImage == "" {
		t.Errorf("DefaultImage must be non-empty (default for 'Install at startup' radio)")
	}
	if !strings.Contains(a.DefaultSetup, "claude.ai/install.sh") {
		t.Errorf("DefaultSetup should reference the native installer URL; got: %s", a.DefaultSetup)
	}
	if !strings.Contains(a.LaunchCmd, "claude") {
		t.Errorf("LaunchCmd should invoke the claude binary; got: %s", a.LaunchCmd)
	}
	if !strings.Contains(a.LaunchCmd, "while") {
		t.Errorf("LaunchCmd should auto-restart on exit (while loop); got: %s", a.LaunchCmd)
	}
	if a.Env["USE_BUILTIN_RIPGREP"] != "0" {
		t.Errorf("Env should set USE_BUILTIN_RIPGREP=0 for Alpine musl; got: %v", a.Env)
	}
}

func TestService_Create_WithCustomImage(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	req := CreateRequest{Agent: "claude", Image: "my-registry/custom-claude:v1.2.3"}
	sess, err := svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Image != "my-registry/custom-claude:v1.2.3" {
		t.Errorf("Image: want override to flow through, got %q", sess.Image)
	}

	// Round-trip through the DB; Get should return the same image.
	got, err := svc.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Image != "my-registry/custom-claude:v1.2.3" {
		t.Errorf("Image after Get: want override preserved, got %q", got.Image)
	}
}

func TestService_Create_WithCustomSetupScript(t *testing.T) {
	svc, fake, _, _ := newTestService(t)

	customSetup := `echo "custom setup" > /tmp/custom-marker`
	req := CreateRequest{Agent: "claude", SetupScript: customSetup}
	sess, err := svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.SetupScript != customSetup {
		t.Errorf("SetupScript verbatim: want %q, got %q", customSetup, sess.SetupScript)
	}

	// The fake sandbox records the spec it received on Create. Verify the entrypoint
	// includes the custom setup script; drift here would mean user-supplied setup
	// silently gets ignored.
	if len(fake.specs) == 0 {
		t.Fatal("fake didn't record any Create specs")
	}
	cmd := strings.Join(fake.specs[0].Cmd, " ")
	if !strings.Contains(cmd, customSetup) {
		t.Errorf("entrypoint should embed the custom setup script verbatim; got: %s", cmd)
	}
	// And NOT the agent's default setup, since the user overrode it.
	if strings.Contains(cmd, "claude.ai/install.sh") {
		t.Errorf("user override should replace agent default, but DefaultSetup still appears in entrypoint")
	}
}

func TestService_Create_DefaultsToAgentRegistry(t *testing.T) {
	// Blank Image + blank SetupScript should pick up agent.DefaultImage and inline
	// agent.DefaultSetup in the entrypoint.
	svc, fake, _, _ := newTestService(t)

	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a, _ := resolveAgent("claude")
	if sess.Image != a.DefaultImage {
		t.Errorf("Image: want agent default %q, got %q", a.DefaultImage, sess.Image)
	}
	// SetupScript stays empty on the row (sentinel for "use agent default at runtime").
	if sess.SetupScript != "" {
		t.Errorf("SetupScript: want empty sentinel, got %q", sess.SetupScript)
	}
	// But the entrypoint should contain the agent's default setup commands.
	if len(fake.specs) == 0 {
		t.Fatal("fake didn't record any Create specs")
	}
	cmd := strings.Join(fake.specs[0].Cmd, " ")
	if !strings.Contains(cmd, "claude.ai/install.sh") {
		t.Errorf("entrypoint should inline agent.DefaultSetup; got: %s", cmd)
	}
}

func TestService_Create_MotdInEntrypoint(t *testing.T) {
	// The MOTD heredoc in the preamble is what SSH-mode shell windows cat at attach
	// time. If the preamble drifts (e.g. accidentally moved into the user-editable
	// setup), users could remove it.
	svc, fake, _, _ := newTestService(t)

	if _, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "motd-test"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fake.specs) == 0 {
		t.Fatal("fake didn't record any Create specs")
	}
	cmd := strings.Join(fake.specs[0].Cmd, " ")
	if !strings.Contains(cmd, "/etc/deepcove-motd") {
		t.Errorf("entrypoint should write /etc/deepcove-motd; got: %s", cmd)
	}
	if !strings.Contains(cmd, `Welcome to deepcove session "motd-test"`) {
		t.Errorf("MOTD should embed the session name; got: %s", cmd)
	}
	if !strings.Contains(cmd, "/workspace") || !strings.Contains(cmd, "/root") {
		t.Errorf("MOTD should mention the /workspace and /root paths; got: %s", cmd)
	}
}

func TestService_Create_PersistsMounts(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	req := CreateRequest{
		Agent: "claude",
		Mounts: []MountRequest{
			{HostPath: "/Users/x/a", ContainerPath: "/in-ct/a", Mode: "rw"},
			{HostPath: "/Users/x/b", ContainerPath: "/in-ct/b", Mode: "ro"},
		},
	}
	sess, err := svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Mounts) != 2 {
		t.Fatalf("Mounts len: want 2, got %d (%+v)", len(got.Mounts), got.Mounts)
	}
	if got.Mounts[0].HostPath != "/Users/x/a" || got.Mounts[0].Mode != "rw" {
		t.Errorf("Mounts[0]: %+v", got.Mounts[0])
	}
	if got.Mounts[1].HostPath != "/Users/x/b" || got.Mounts[1].Mode != "ro" {
		t.Errorf("Mounts[1]: %+v", got.Mounts[1])
	}
}

func TestService_Destroy_DoesNotRemoveSharedAgentVolume(t *testing.T) {
	// The agent-home volume is shared across all sessions of the same agent; it holds the user's login
	// and the installed agent binary. Destroying one claude session must not clobber it, because another
	// claude session created later expects to still be logged in.
	//
	// Per SESSION storage is a different thing and does go: the VM runtime keeps a home disk named after
	// the session, and nothing else will ever claim it.
	svc, fake, _, _ := newTestService(t)
	sess := mustCreate(t, svc, "claude")

	if err := svc.Destroy(context.Background(), sess.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if slices.Contains(fake.removedStorage, agentHomeVolumeName("claude")) {
		t.Errorf("the shared agent volume was removed: %v", fake.removedStorage)
	}
	if !slices.Contains(fake.removedStorage, sess.ID) {
		t.Errorf("per session storage was not removed; got %v, want it to contain %q", fake.removedStorage, sess.ID)
	}
}

func TestService_Create_UsesPerAgentSharedVolume(t *testing.T) {
	// Pin the shared-volume behavior: two sessions of the same agent must reference the
	// same volume name, so Docker mounts the same persistent storage and both see the
	// same /root/.claude and /root/.local/bin/claude.
	svc, fake, _, _ := newTestService(t)
	a, _ := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "share-a"})
	b, _ := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "share-b"})

	if len(fake.specs) < 2 {
		t.Fatalf("expected 2 Create specs, got %d", len(fake.specs))
	}
	homeName := agentHomeVolumeName("claude")
	for i, spec := range fake.specs[:2] {
		found := false
		for _, m := range spec.Mounts {
			if m.Target == agentHomeMountTarget && m.Source == homeName {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("session %d (%s/%s): expected agent-home mount Source=%q Target=%q in spec.Mounts=%v",
				i, []string{a.ID, b.ID}[i], []string{a.Name, b.Name}[i], homeName, agentHomeMountTarget, spec.Mounts)
		}
	}
}

func TestService_Create_WorkdirAndNoScratch(t *testing.T) {
	// No deepcove-managed scratch mount anymore: a no-mount session's spec carries
	// only the agent-home volume (no bind targeting /workspace), and DEEPCOVE_WORKDIR
	// defaults to /workspace. A session with a user mount points DEEPCOVE_WORKDIR at
	// the first mount's target.
	tests := []struct {
		name                 string
		mounts               []MountRequest
		wantWorkdir          string
		wantNoWorkspaceMount bool // assert the spec has no mount targeting /workspace
	}{
		{
			name:                 "no mounts: workdir defaults to /workspace, no scratch bind",
			mounts:               nil,
			wantWorkdir:          "/workspace",
			wantNoWorkspaceMount: true,
		},
		{
			name:        "first user mount drives the workdir",
			mounts:      []MountRequest{{HostPath: "/Users/x/code", ContainerPath: "/code"}},
			wantWorkdir: "/code",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			if _, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Mounts: tc.mounts}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			spec := fake.specs[len(fake.specs)-1]
			if got := spec.Env["DEEPCOVE_WORKDIR"]; got != tc.wantWorkdir {
				t.Errorf("DEEPCOVE_WORKDIR: want %q, got %q", tc.wantWorkdir, got)
			}
			if tc.wantNoWorkspaceMount {
				for _, m := range spec.Mounts {
					if m.Target == "/workspace" {
						t.Errorf("expected no mount targeting /workspace; got %+v", m)
					}
				}
			}
		})
	}
}

func TestService_EditMounts(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(t *testing.T, svc *Service) string // returns session id
		newMounts       []MountRequest
		wantErrIs       error
		wantErrContains string
		wantSandboxOps  string // expected sandbox call sequence DURING EditMounts (excludes setup)
		wantMountsLen   int
	}{
		{
			name: "happy path: replaces mounts on a running session (stop, destroy, create, start)",
			setup: func(t *testing.T, svc *Service) string {
				return mustCreate(t, svc, "claude").ID
			},
			newMounts: []MountRequest{
				{HostPath: "/Users/x/repo", ContainerPath: "/workspace"},
			},
			wantSandboxOps: "Stop,Destroy,Create,Start",
			wantMountsLen:  1,
		},
		{
			name: "stopped session: skips stop, recreates without start",
			setup: func(t *testing.T, svc *Service) string {
				sess := mustCreate(t, svc, "claude")
				if err := svc.Stop(context.Background(), sess.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
				return sess.ID
			},
			newMounts: []MountRequest{
				{HostPath: "/Users/x/repo", ContainerPath: "/workspace"},
			},
			wantSandboxOps: "Destroy,Create",
			wantMountsLen:  1,
		},
		{
			name: "invalid mount rejected before any sandbox work",
			setup: func(t *testing.T, svc *Service) string {
				return mustCreate(t, svc, "claude").ID
			},
			newMounts: []MountRequest{
				{HostPath: "/x", ContainerPath: "/root"}, // reserved (agent home)
			},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "reserved for the agent home",
			wantSandboxOps:  "",
			wantMountsLen:   0, // not relevant; we check err
		},
		{
			name: "unknown session id returns ErrNotFound",
			setup: func(t *testing.T, svc *Service) string {
				return "no-such-id"
			},
			newMounts:      nil,
			wantErrIs:      ErrNotFound,
			wantSandboxOps: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newTestService(t)
			id := tc.setup(t, svc)
			// Reset call sequence after setup so the assertion only
			// reflects what EditMounts itself did.
			fake.mu.Lock()
			fake.calls = nil
			fake.mu.Unlock()

			updated, err := svc.EditMounts(context.Background(), id, tc.newMounts)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err: want errors.Is(_, %v), got %v", tc.wantErrIs, err)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("err message: want substring %q, got %q", tc.wantErrContains, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("EditMounts: %v", err)
				}
				if updated == nil {
					t.Fatal("got nil session on success")
				}
				if len(updated.Mounts) != tc.wantMountsLen {
					t.Errorf("updated.Mounts len: want %d, got %d (%+v)", tc.wantMountsLen, len(updated.Mounts), updated.Mounts)
				}
				if updated.SandboxID == "" {
					t.Errorf("updated.SandboxID should be non-empty after recreate")
				}
			}

			if got := fake.callSequence(); got != tc.wantSandboxOps {
				t.Errorf("sandbox ops during EditMounts: want %q, got %q", tc.wantSandboxOps, got)
			}
		})
	}
}

func TestValidateMountRequests(t *testing.T) {
	tests := []struct {
		name             string
		reqs             []MountRequest
		wantErrIs        error    // matched via errors.Is when non-nil
		wantErrContains  string   // substring match; empty = no substring requirement
		wantLen          int      // expected len of returned []sandbox.Mount
		wantReadOnlyMask []bool   // per-index ReadOnly assertion; nil = skip
		wantTargets      []string // per-index Target assertion (cleaned); nil = skip
	}{
		{
			name:    "nil input is fine, sessions without user mounts",
			reqs:    nil,
			wantLen: 0,
		},
		{
			name: "happy path: single RW mount",
			reqs: []MountRequest{
				{HostPath: "/Users/foo/proj", ContainerPath: "/deepcove"},
			},
			wantLen:          1,
			wantReadOnlyMask: []bool{false},
			wantTargets:      []string{"/deepcove"},
		},
		{
			name: "mode rw and ro both accepted; default empty = rw",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/x", Mode: "rw"},
				{HostPath: "/b", ContainerPath: "/y", Mode: "ro"},
				{HostPath: "/c", ContainerPath: "/z", Mode: ""},
				{HostPath: "/d", ContainerPath: "/q", Mode: "RO"}, // case-insensitive
			},
			wantLen:          4,
			wantReadOnlyMask: []bool{false, true, false, true},
		},
		{
			name: "container_path cleaned to canonical form on output",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/foo/bar/../bar"},
			},
			wantLen:     1,
			wantTargets: []string{"/foo/bar"},
		},
		{
			name:            "empty host_path rejects",
			reqs:            []MountRequest{{HostPath: "", ContainerPath: "/x"}},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "host_path is required",
		},
		{
			name:            "empty container_path rejects",
			reqs:            []MountRequest{{HostPath: "/a", ContainerPath: ""}},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "container_path is required",
		},
		{
			name: "container_path = /workspace is allowed (the conventional target)",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/workspace"},
			},
			wantLen:     1,
			wantTargets: []string{"/workspace"},
		},
		{
			name: "container_path = /root reserved for the agent home",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/root"},
			},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "reserved for the agent home",
		},
		{
			name: "container_path that cleans to /root also rejected",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/root/"},
			},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "reserved for the agent home",
		},
		{
			name: "container_path = / rejected",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/"},
			},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "container root",
		},
		{
			name: "duplicate container_path rejected",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/deepcove"},
				{HostPath: "/b", ContainerPath: "/deepcove"},
			},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: "also used by mounts[0]",
		},
		{
			name: "invalid mode rejected",
			reqs: []MountRequest{
				{HostPath: "/a", ContainerPath: "/x", Mode: "readonly"},
			},
			wantErrIs:       ErrMountInvalid,
			wantErrContains: `must be "rw" or "ro"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateMountRequests(tc.reqs)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err: want errors.Is(_, %v), got %v", tc.wantErrIs, err)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("err message: want substring %q, got %q", tc.wantErrContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil err, got %v", err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("len: want %d, got %d (%+v)", tc.wantLen, len(got), got)
			}
			for i, want := range tc.wantReadOnlyMask {
				if i >= len(got) {
					t.Errorf("ReadOnly[%d]: out of range (only %d entries)", i, len(got))
					continue
				}
				if got[i].ReadOnly != want {
					t.Errorf("ReadOnly[%d]: want %v, got %v", i, want, got[i].ReadOnly)
				}
			}
			for i, want := range tc.wantTargets {
				if i >= len(got) {
					t.Errorf("Target[%d]: out of range", i)
					continue
				}
				if got[i].Target != want {
					t.Errorf("Target[%d]: want %q, got %q", i, want, got[i].Target)
				}
			}
		})
	}
}

func TestAgents(t *testing.T) {
	got := Agents()

	// Whole-list invariants, checked once, not per case.
	if len(got) == 0 {
		t.Fatal("Agents() returned empty list")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Errorf("not sorted: %q before %q", got[i-1].Name, got[i].Name)
		}
	}

	// v1: Claude only. Other agents (codex, gemini, opencode) are deferred and must NOT appear in the
	// public registry. If you're adding a new agent, append it here AND verify the install/launch
	// end-to-end first.
	tests := []struct {
		agent string
	}{
		{agent: "claude"},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			for _, a := range got {
				if a.Name == tc.agent {
					if a.DefaultImage == "" {
						t.Errorf("agent %q has empty DefaultImage", tc.agent)
					}
					return
				}
			}
			t.Errorf("agent %q not in list", tc.agent)
		})
	}
}

// TestStartingStatus covers the in-flight status: it is reported while a start runs, and it disappears
// when the start finishes.
func TestStartingStatus(t *testing.T) {
	cases := []struct {
		name     string
		row      Status
		inFlight bool
		want     Status
	}{
		{name: "a stopped session with a start in flight reads as starting", row: StatusStopped, inFlight: true, want: StatusStarting},
		{name: "a new session with a start in flight reads as starting", row: StatusCreated, inFlight: true, want: StatusStarting},
		{name: "a running session reads as starting while its machine restarts", row: StatusRunning, inFlight: true, want: StatusStarting},
		{name: "no start in flight leaves the row alone", row: StatusStopped, inFlight: false, want: StatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{}
			if tc.inFlight {
				if _, ok := svc.beginStarting("s1"); !ok {
					t.Fatal("beginStarting refused the first start")
				}
			}
			sess := &Session{ID: "s1", Status: tc.row}
			svc.reportStarting(sess)
			if sess.Status != tc.want {
				t.Errorf("status: got %q, want %q", sess.Status, tc.want)
			}
		})
	}

	t.Run("a second start is refused while one is in flight", func(t *testing.T) {
		svc := &Service{}
		done, ok := svc.beginStarting("s1")
		if !ok {
			t.Fatal("beginStarting refused the first start")
		}
		if _, ok := svc.beginStarting("s1"); ok {
			t.Fatal("beginStarting allowed two starts of one session")
		}
		// A different session is unaffected.
		if _, ok := svc.beginStarting("s2"); !ok {
			t.Fatal("beginStarting refused an unrelated session")
		}

		done()
		if _, ok := svc.beginStarting("s1"); !ok {
			t.Fatal("beginStarting still refused after the start finished")
		}
		sess := &Session{ID: "s1", Status: StatusStopped}
		svc.reportStarting(sess)
		if sess.Status != StatusStarting {
			t.Errorf("status after restarting: got %q, want %q", sess.Status, StatusStarting)
		}
	})
}
