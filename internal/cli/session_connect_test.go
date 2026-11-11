package cli

import "testing"

func TestFindDetachKey(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		wantInput    string
		wantIsDetach bool
	}{
		{name: "typing", input: "hello", wantInput: "hello"},
		{name: "detach key alone", input: "\x1c", wantIsDetach: true},
		{name: "typed before the key", input: "ls\x1c", wantInput: "ls", wantIsDetach: true},
		{name: "typed after the key is dropped", input: "a\x1cb", wantInput: "a", wantIsDetach: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, isDetach := findDetachKey([]byte(tc.input))
			if string(input) != tc.wantInput || isDetach != tc.wantIsDetach {
				t.Errorf("findDetachKey(%q) = %q, %v, want %q, %v", tc.input, input, isDetach, tc.wantInput, tc.wantIsDetach)
			}
		})
	}
}
