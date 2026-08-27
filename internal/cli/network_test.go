package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/connect"
	"github.com/waltzofpearls/deepcove/internal/tailnet"
)

// mockNodeServer answers /api/connect/status with the given statuses in order, repeating the last one,
// so a test can walk a node from starting to running the way a real sign-in does.
func mockNodeServer(t *testing.T, statuses []connect.Status) *httptest.Server {
	t.Helper()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := min(calls, len(statuses)-1)
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statuses[i])
	}))
	t.Cleanup(server.Close)
	return server
}

func testClient(server *httptest.Server) *apiClient {
	return &apiClient{baseURL: server.URL, http: server.Client()}
}

func TestWaitForNetwork(t *testing.T) {
	cases := []struct {
		name       string
		statuses   []connect.Status
		wantURL    string
		wantURLs   int
		wantErr    string
		waitCancel bool
	}{
		{
			name: "shows the url once and returns when running",
			statuses: []connect.Status{
				{Phase: string(tailnet.PhaseNeedsLogin), LoginURL: "https://login.tailscale.com/a/abc"},
				{Phase: string(tailnet.PhaseNeedsLogin), LoginURL: "https://login.tailscale.com/a/abc"},
				{Phase: string(tailnet.PhaseRunning), Name: "deepcove-droplet"},
			},
			wantURL:  "https://login.tailscale.com/a/abc",
			wantURLs: 1,
		},
		{
			name:     "already running needs no url",
			statuses: []connect.Status{{Phase: string(tailnet.PhaseRunning)}},
			wantURLs: 0,
		},
		{
			name:       "gives up when the wait is cancelled",
			statuses:   []connect.Status{{Phase: string(tailnet.PhaseNeedsLogin), LoginURL: "https://x"}},
			wantURLs:   1,
			waitCancel: true,
			wantErr:    "stopped waiting",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := mockNodeServer(t, tc.statuses)
			client := testClient(server)

			var urls []string
			ctx := context.Background()
			if tc.waitCancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}

			status, err := client.waitForNetwork(ctx, &tc.statuses[0], time.Millisecond,
				func(u string) { urls = append(urls, u) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("waitForNetwork: %v", err)
				}
				if status.Phase != string(tailnet.PhaseRunning) {
					t.Errorf("phase = %q, want running", status.Phase)
				}
			}
			if len(urls) != tc.wantURLs {
				t.Fatalf("login url shown %d times, want %d", len(urls), tc.wantURLs)
			}
			if tc.wantURL != "" && urls[0] != tc.wantURL {
				t.Errorf("url = %q, want %q", urls[0], tc.wantURL)
			}
		})
	}
}

func TestNetworkCallReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tailnet is not configured", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	_, err := testClient(server).NetworkStatus(context.Background())
	if err == nil {
		t.Fatal("wanted an error from a 500")
	}
	if !strings.Contains(err.Error(), "tailnet is not configured") {
		t.Errorf("err = %v, want the server's message to survive", err)
	}
}

func TestNetworkCallSaysWhenNothingIsRunning(t *testing.T) {
	// A closed server stands in for no node at all, which is what someone meets before `deepcove start`.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := testClient(server)
	server.Close()

	_, err := client.NetworkStatus(context.Background())
	if err == nil {
		t.Fatal("wanted a dial error")
	}
	if !strings.Contains(err.Error(), "deepcove start") {
		t.Errorf("err = %v, want it to say how to start the node", err)
	}
}

func TestConnectNodeSendsTheAuthKey(t *testing.T) {
	cases := []struct {
		name     string
		authKey  string
		wantBody string
	}{
		{name: "with a key", authKey: "tskey-test", wantBody: `{"auth_key":"tskey-test"}`},
		{name: "without one sends no body", authKey: "", wantBody: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				got = strings.TrimSpace(string(body))
				_ = json.NewEncoder(w).Encode(connect.Status{Phase: string(tailnet.PhaseRunning)})
			}))
			t.Cleanup(server.Close)

			if _, err := testClient(server).connectNode(context.Background(), tc.authKey); err != nil {
				t.Fatalf("connectNode: %v", err)
			}
			if got != tc.wantBody {
				t.Errorf("request body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}
