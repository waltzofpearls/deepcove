package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestFindCommand(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFound bool
		wantArgs  []string
	}{
		{name: "connect", args: []string{"auth"}, wantFound: true, wantArgs: []string{"auth"}},
		{name: "ls", args: []string{"-a"}, wantFound: true, wantArgs: []string{"-a"}},
		{name: "session", args: []string{"claude"}, wantFound: true, wantArgs: []string{"claude"}},
		{name: "claude", args: []string{"--name", "auth"}, wantFound: true, wantArgs: []string{"claude", "--name", "auth"}},
		{name: "codex", wantFound: true, wantArgs: []string{"codex"}},
		{name: "nope", args: []string{"x"}, wantFound: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, args, found := findCommand(tc.name, tc.args)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if found && run == nil {
				t.Fatal("found a command with nothing to run")
			}
			if !slices.Equal(args, tc.wantArgs) {
				t.Errorf("args = %q, want %q", args, tc.wantArgs)
			}
		})
	}
}

func TestHasHelpArg(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "nothing", args: nil, want: false},
		{name: "-h", args: []string{"-h"}, want: true},
		{name: "--help", args: []string{"--help"}, want: true},
		{name: "-help", args: []string{"-help"}, want: true},
		{name: "--h", args: []string{"--h"}, want: true},
		{name: "with a value", args: []string{"--help=true"}, want: true},
		{name: "with a false value", args: []string{"-h=false"}, want: true},
		{name: "after other flags", args: []string{"--timeout", "1m", "-h"}, want: true},
		{name: "a value that only contains it", args: []string{"KEY=-h"}, want: false},
		{name: "after -- belongs to deepcove start", args: []string{"--", "-h"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasHelpArg(tc.args); got != tc.want {
				t.Errorf("hasHelpArg(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestRouters_ShowHelpInsteadOfRunningTheVerb(t *testing.T) {
	// Anything that reaches this server means a verb ran instead of showing its help.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s %s reached deepcove", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("ADDR", strings.TrimPrefix(server.URL, "http://"))

	cases := []struct {
		name string
		run  func(context.Context, []string) error
		args []string
	}{
		{name: "network logout", run: cmdNetwork, args: []string{"logout", "-h"}},
		{name: "network logout with a value", run: cmdNetwork, args: []string{"logout", "--help=true"}},
		{name: "network disconnect", run: cmdNetwork, args: []string{"disconnect", "--help"}},
		{name: "network login", run: cmdNetwork, args: []string{"login", "-h"}},
		{name: "db repair", run: cmdDB, args: []string{"repair", "-h"}},
		{name: "db check", run: cmdDB, args: []string{"check", "-h"}},
		{name: "config set", run: cmdConfig, args: []string{"set", "-h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(context.Background(), tc.args); err != nil {
				t.Errorf("%q = %v, want its help and no error", tc.args, err)
			}
		})
	}
}
