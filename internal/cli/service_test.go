package cli

import (
	"slices"
	"testing"
)

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
			args:       []string{"--no-linger", "--", "--cluster", "prod"},
			wantBefore: []string{"--no-linger"},
			wantAfter:  []string{"--cluster", "prod"},
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
