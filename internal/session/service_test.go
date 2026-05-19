package session

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// openTestDB opens an in-memory DuckDB and creates the sessions
// schema. Mirrors the production migration but inline so tests don't
// depend on the repository package's embed setup.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE sessions (
			id               TEXT PRIMARY KEY,
			name             TEXT NOT NULL UNIQUE,
			agent            TEXT NOT NULL,
			image            TEXT NOT NULL,
			owner_email      TEXT NOT NULL,
			status           TEXT NOT NULL DEFAULT 'created',
			mounts           TEXT NOT NULL DEFAULT '[]',
			env              TEXT NOT NULL DEFAULT '{}',
			container_id     TEXT,
			created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_activity_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create sessions: %v", err)
	}
	return db
}

// newTestService spins up a Service backed by an in-memory DuckDB
// session repo and a fake sandbox. Returns the *sql.DB too so tests
// that need to inspect the underlying table (e.g. rowCount) can do
// so directly — that's a test-only escape hatch, not a public API.
func newTestService(t *testing.T) (*Service, *fakeSandbox, string, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	fake := newFakeSandbox()
	dataDir := t.TempDir()
	svc := NewService(NewDuckDBSessionRepository(db), fake, dataDir, nil)
	return svc, fake, dataDir, db
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
// helper — kept per-package because each package has slightly
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

// rowCount counts rows in the sessions table — used by several tables
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
			wantCallSequence: "Create,Start",
			wantLive:         1,
			wantRows:         1,
			wantStatus:       StatusRunning,
		},
		{
			name:             "happy path with a different known agent",
			req:              CreateRequest{Agent: "codex"},
			wantCallSequence: "Create,Start",
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
			wantCallSequence: "Create,Start,Destroy",
			wantLive:         0,
			wantRows:         0,
		},
		{
			name:             "sandbox create failure stops before persist",
			req:              CreateRequest{Agent: "claude"},
			createErr:        errors.New("daemon-down"),
			wantErrContains:  "daemon-down",
			wantCallSequence: "Create",
			wantLive:         0,
			wantRows:         0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, dataDir, db := newTestService(t)
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

			// On success the session should be running, named (auto
			// or explicit), and have a workspace dir on disk.
			if tc.wantRows > 0 && sess != nil {
				if sess.Status != tc.wantStatus {
					t.Errorf("status: want %q, got %q", tc.wantStatus, sess.Status)
				}
				if sess.Name == "" {
					t.Errorf("name not populated")
				}
				if !strings.HasPrefix(sess.Name, tc.req.Agent+"-") {
					t.Errorf("auto-name should start with agent prefix; got %q", sess.Name)
				}
				if !pathExists(filepath.Join(dataDir, "sessions", sess.ID)) {
					t.Errorf("workspace dir not created")
				}
			} else if sess != nil {
				t.Errorf("expected nil session on failure, got %+v", sess)
			}

			// Rollback test must leave no workspace orphans.
			if tc.wantRows == 0 {
				entries, _ := filepath.Glob(filepath.Join(dataDir, "sessions", "*"))
				if len(entries) != 0 {
					t.Errorf("expected empty workspace dir on failure; found %v", entries)
				}
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
				_ = mustCreate(t, svc, "claude")
				stopped := mustCreate(t, svc, "codex")
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
				_ = mustCreate(t, svc, "claude")
				stopped := mustCreate(t, svc, "codex")
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
		name           string
		setupID        func(t *testing.T, svc *Service) string
		wantErrIs      error
		wantWorkspace  bool // true = workspace should remain after Destroy (e.g. setup didn't create one)
		wantRowsAfter  int
		wantLiveAfter  int
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

func TestAgents(t *testing.T) {
	got := Agents()

	// Whole-list invariants — checked once, not per case.
	if len(got) == 0 {
		t.Fatal("Agents() returned empty list")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Errorf("not sorted: %q before %q", got[i-1].Name, got[i].Name)
		}
	}

	tests := []struct {
		agent string
	}{
		{agent: "claude"},
		{agent: "codex"},
		{agent: "gemini"},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			for _, a := range got {
				if a.Name == tc.agent {
					if a.Image == "" {
						t.Errorf("agent %q has empty Image", tc.agent)
					}
					return
				}
			}
			t.Errorf("agent %q not in list", tc.agent)
		})
	}
}
