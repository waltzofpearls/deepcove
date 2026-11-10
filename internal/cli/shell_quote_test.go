package cli

import "testing"

func TestBuildShellArgument(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		want string
	}{
		{name: "plain path", arg: "/home/deep/.local/share/deepcove/logs/deepcove.log", want: "/home/deep/.local/share/deepcove/logs/deepcove.log"},
		{name: "space", arg: "/Users/me/Library/Application Support/deepcove.log", want: "'/Users/me/Library/Application Support/deepcove.log'"},
		{name: "single quote", arg: "/tmp/it's.log", want: `'/tmp/it'\''s.log'`},
		{name: "empty", arg: "", want: "''"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildShellArgument(tc.arg); got != tc.want {
				t.Errorf("buildShellArgument(%q) = %s, want %s", tc.arg, got, tc.want)
			}
		})
	}
}

func TestBuildShellCommand(t *testing.T) {
	got := buildShellCommand([]string{"tail", "-f", "/Users/me/Application Support/deepcove.log"})
	want := "tail -f '/Users/me/Application Support/deepcove.log'"
	if got != want {
		t.Errorf("buildShellCommand = %s, want %s", got, want)
	}
}
