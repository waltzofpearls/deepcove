package cli

import (
	"slices"
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
