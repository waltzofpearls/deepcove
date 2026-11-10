package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/connect"
	"github.com/waltzofpearls/deepcove/internal/service"
	"github.com/waltzofpearls/deepcove/internal/session"
)

func TestFindStatusReport(t *testing.T) {
	onTailnet := &connect.Status{Phase: "running", Name: "deepcove-mac.tail1.ts.net"}
	logCommand := []string{"tail", "-f", "/tmp/deepcove.log"}
	foreground := statusReport{
		isRunning:         true,
		version:           "v1.2.3",
		startCommand:      "deepcove service start",
		network:           onTailnet,
		localSessionCount: 1,
		isEveryNodeListed: true,
		everySessionCount: 2,
	}
	asService := foreground
	asService.isService = true
	asService.logCommand = logCommand
	cases := []struct {
		name          string
		isStopped     bool
		isStalled     bool
		serviceStatus service.Status
		want          statusReport
	}{
		{
			name:          "foreground next to a running service",
			serviceStatus: service.Status{Installed: true, Running: true, PID: 7, LogCommand: logCommand},
			want:          foreground,
		},
		{
			name:          "service",
			serviceStatus: service.Status{Installed: true, Running: true, PID: 42, LogCommand: logCommand},
			want:          asService,
		},
		{
			name:          "stopped",
			isStopped:     true,
			serviceStatus: service.Status{Installed: true},
			want:          statusReport{startCommand: "deepcove service start"},
		},
		{
			name:          "service running but not answering here",
			isStopped:     true,
			serviceStatus: service.Status{Installed: true, Running: true, PID: 7},
			want:          statusReport{startCommand: "deepcove service start", isServiceRunning: true},
		},
		{
			name:          "health stalls",
			isStalled:     true,
			serviceStatus: service.Status{Installed: true},
			want:          statusReport{startCommand: "deepcove service start", isNotAnswering: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/health":
					if tc.isStalled {
						<-r.Context().Done()
						return
					}
					_ = json.NewEncoder(w).Encode(nodeHealth{Version: "v1.2.3", PID: 42})
				case "/api/sessions/local":
					_ = json.NewEncoder(w).Encode([]*session.Session{{ID: "a"}})
				case "/api/sessions":
					_ = json.NewEncoder(w).Encode(session.SessionListResponse{Sessions: []*session.Session{{ID: "a"}, {ID: "b"}}})
				case "/api/connect/status":
					_ = json.NewEncoder(w).Encode(onTailnet)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			httpClient := srv.Client()
			httpClient.Timeout = 100 * time.Millisecond
			client := &apiClient{baseURL: srv.URL, http: httpClient}
			if tc.isStopped {
				srv.Close()
			}

			if got := findStatusReport(t.Context(), client, tc.serviceStatus); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findStatusReport =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func TestBuildStatusLines(t *testing.T) {
	onTailnet := &connect.Status{
		Phase:   "running",
		Name:    "deepcove-mac.tail1.ts.net",
		Tailnet: "me@example.com",
		URL:     "https://deepcove-mac.tail1.ts.net",
	}
	cases := []struct {
		name   string
		report statusReport
		want   []string
	}{
		{
			name:   "not running",
			report: statusReport{startCommand: "deepcove service start"},
			want: []string{
				"deepcove:  not running",
				"start it:  deepcove service start",
			},
		},
		{
			name: "foreground",
			report: statusReport{
				isRunning:         true,
				version:           "v1.2.3",
				network:           onTailnet,
				localSessionCount: 2,
				isEveryNodeListed: true,
				everySessionCount: 2,
			},
			want: []string{
				"deepcove:  running in the foreground, v1.2.3",
				"open it:   deepcove open",
				"network:   deepcove-mac.tail1.ts.net (me@example.com)",
				"url:       https://deepcove-mac.tail1.ts.net",
				"sessions:  2 running, 2 on this node",
			},
		},
		{
			name: "service",
			report: statusReport{
				isRunning:         true,
				version:           "v1.2.3",
				isService:         true,
				network:           onTailnet,
				localSessionCount: 1,
				isEveryNodeListed: true,
				everySessionCount: 6,
				logCommand:        []string{"tail", "-f", "/Users/me/Application Support/deepcove.log"},
			},
			want: []string{
				"deepcove:  running as a service, v1.2.3",
				"open it:   deepcove open",
				"network:   deepcove-mac.tail1.ts.net (me@example.com)",
				"url:       https://deepcove-mac.tail1.ts.net",
				"sessions:  6 running, 1 on this node",
				"read logs: tail -f '/Users/me/Application Support/deepcove.log'",
			},
		},
		{
			name:   "network not logged in",
			report: statusReport{isRunning: true, version: "v1.2.3", network: &connect.Status{Phase: "needs_login"}},
			want: []string{
				"deepcove:  running in the foreground, v1.2.3",
				"open it:   deepcove open",
				"network:   needs_login",
				"sessions:  0 running on this node",
			},
		},
		{
			name:   "not answering",
			report: statusReport{isNotAnswering: true},
			want:   []string{"deepcove:  running but not answering, see `deepcove service status` for its logs"},
		},
		{
			name:   "service running but not answering here",
			report: statusReport{isServiceRunning: true, startCommand: "deepcove service start"},
			want:   []string{"deepcove:  not answering here, but the service is running, see `deepcove service status`"},
		},
		{
			name:   "session error",
			report: statusReport{isRunning: true, version: "v1.2.3", sessionError: "database is locked"},
			want: []string{
				"deepcove:  running in the foreground, v1.2.3",
				"open it:   deepcove open",
				"sessions:  could not list them: database is locked",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildStatusLines(tc.report); !slices.Equal(got, tc.want) {
				t.Errorf("buildStatusLines =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestBuildSessionCount(t *testing.T) {
	cases := []struct {
		name   string
		report statusReport
		want   string
	}{
		{
			name:   "every node listed",
			report: statusReport{localSessionCount: 1, isEveryNodeListed: true, everySessionCount: 6},
			want:   "6 running, 1 on this node",
		},
		{
			name: "a node did not answer",
			report: statusReport{
				localSessionCount:    1,
				isEveryNodeListed:    true,
				everySessionCount:    4,
				unreachableNodeCount: 1,
			},
			want: "4 running, 1 on this node, 1 node(s) did not answer",
		},
		{
			name:   "only this node",
			report: statusReport{localSessionCount: 1},
			want:   "1 running on this node",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildSessionCount(tc.report); got != tc.want {
				t.Errorf("buildSessionCount = %q, want %q", got, tc.want)
			}
		})
	}
}
