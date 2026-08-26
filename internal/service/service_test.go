package service

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records what the manager asked the platform to do, in order, so install and uninstall can
// be tested without touching launchd or systemd.
type fakeRunner struct {
	calls  []string
	fail   map[string]bool
	stdout map[string]string
}

var errFake = errors.New("fake runner: command failed")

func newFakeRunner() *fakeRunner {
	return &fakeRunner{fail: map[string]bool{}, stdout: map[string]string{}}
}

func (f *fakeRunner) run(name string, args ...string) error {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	if f.fail[name] {
		return errFake
	}
	return nil
}

func (f *fakeRunner) output(name string, args ...string) (string, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	return f.stdout[name], nil
}

func TestStartCommand(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		want  []string
	}{
		{
			name: "no flags",
			want: []string{"/usr/local/bin/deepcove", "start"},
		},
		{
			name:  "the flags asked for at install time follow start",
			extra: []string{"--cluster", "prod", "--addr", "127.0.0.1:9090"},
			want:  []string{"/usr/local/bin/deepcove", "start", "--cluster", "prod", "--addr", "127.0.0.1:9090"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := startCommand("/usr/local/bin/deepcove", tc.extra)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
