package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestServiceStoppedMessage(t *testing.T) {
	cases := []struct {
		name   string
		agents int
		want   string
	}{
		{name: "nothing was running", agents: 0, want: "deepcove is stopped."},
		{name: "one agent", agents: 1, want: "1 agent is still running"},
		{name: "more than one", agents: 3, want: "3 agents are still running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serviceStoppedMessage(tc.agents); !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// Everything after `--` belongs to `deepcove start`, and everything before it is ours.
func TestSplitAtDashDash(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantBefore []string
		wantAfter  []string
	}{
		{
			name: "nothing at all",
		},
		{
			name:       "only our own flags",
			args:       []string{"--no-linger"},
			wantBefore: []string{"--no-linger"},
		},
		{
			name:       "flags on both sides",
			args:       []string{"--no-linger", "--", "--addr", "127.0.0.1:9090"},
			wantBefore: []string{"--no-linger"},
			wantAfter:  []string{"--addr", "127.0.0.1:9090"},
		},
		{
			name:      "only flags for start",
			args:      []string{"--", "--addr", "127.0.0.1:9090"},
			wantAfter: []string{"--addr", "127.0.0.1:9090"},
		},
		{
			name:       "a trailing separator passes nothing on",
			args:       []string{"--no-linger", "--"},
			wantBefore: []string{"--no-linger"},
		},
		{
			// The second one belongs to start, which has its own parser for it.
			name:      "only the first separator splits",
			args:      []string{"--", "--addr", "--", "x"},
			wantAfter: []string{"--addr", "--", "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, after := splitAtDashDash(tc.args)
			if !slices.Equal(before, tc.wantBefore) {
				t.Errorf("before = %v, want %v", before, tc.wantBefore)
			}
			if !slices.Equal(after, tc.wantAfter) {
				t.Errorf("after = %v, want %v", after, tc.wantAfter)
			}
		})
	}
}
