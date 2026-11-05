package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/api/middleware"
	"github.com/waltzofpearls/deepcove/internal/service"
)

func TestAskUnlockURL(t *testing.T) {
	cases := []struct {
		name     string
		deepcove func(w http.ResponseWriter, r *http.Request)
		codePath string
		noSocket bool
		wantURL  string
		wantErr  string
	}{
		{
			name: "deepcove answers",
			deepcove: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"url":"http://127.0.0.1:9000/unlock?code=abc"}`))
			},
			codePath: askLinkToOpen,
			wantURL:  "http://127.0.0.1:9000/unlock?code=abc",
		},
		{
			name: "a link to paste",
			deepcove: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has(middleware.PasteQuery) {
					_, _ = w.Write([]byte(`{"url":"http://127.0.0.1:9000/unlock?code=paste"}`))
				}
			},
			codePath: askLinkToPaste,
			wantURL:  "http://127.0.0.1:9000/unlock?code=paste",
		},
		{
			name: "deepcove takes the call and never answers",
			deepcove: func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
			codePath: askLinkToOpen,
			wantErr:  "did not answer",
		},
		{
			name:     "deepcove is not running",
			codePath: askLinkToOpen,
			noSocket: true,
			wantErr:  errNotRunning.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Short, since a Unix socket path has a small length limit and t.TempDir can be long.
			dir, err := os.MkdirTemp("", "dc")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			sock := filepath.Join(dir, "s")
			if !tc.noSocket {
				listener, err := net.Listen("unix", sock)
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{Handler: http.HandlerFunc(tc.deepcove), ReadHeaderTimeout: time.Second}
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(func() { _ = server.Close() })
			}

			started := time.Now()
			unlockURL, err := askUnlockURL(context.Background(), sock, tc.codePath, 200*time.Millisecond)
			if took := time.Since(started); took > 2*time.Second {
				t.Errorf("took %s, want it bounded by the timeout", took)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want it to say %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if unlockURL != tc.wantURL {
				t.Errorf("got %q, want %q", unlockURL, tc.wantURL)
			}
		})
	}
}

func TestFindStartCommand(t *testing.T) {
	want := "deepcove service install"
	if !service.IsSupported() {
		want = "deepcove start"
	}
	if got := findStartCommand(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildNotRunningErrorFrom(t *testing.T) {
	notInstalled := "\n  " + findStartCommand() + "\n"
	cases := []struct {
		name   string
		status service.Status
		want   string
	}{
		{name: "no service installed", want: notInstalled},
		{name: "a stopped service", status: service.Status{Installed: true}, want: "\n  deepcove service start\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildNotRunningErrorFrom(tc.status).Error()
			if !strings.Contains(got, tc.want) || !strings.Contains(got, webNodeURL) {
				t.Errorf("got %q, want it to say %q and %s", got, tc.want, webNodeURL)
			}
		})
	}
}
