package cli

import "testing"

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{51_400_000, "49.0 MiB"},
		{2 << 30, "2.0 GiB"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatBytes(tc.bytes); got != tc.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

func TestProgressLine(t *testing.T) {
	cases := []struct {
		name            string
		complete, total int64
		want            string
	}{
		{"start", 0, 1000, "[                    ]   0%  0 B/1000 B"},
		{"half", 500, 1000, "[==========          ]  50%  500 B/1000 B"},
		{"done", 1000, 1000, "[====================] 100%  1000 B/1000 B"},
		{"no total", 4096, 0, "4.0 KiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := progressLine(tc.complete, tc.total); got != tc.want {
				t.Errorf("progressLine(%d, %d) = %q, want %q", tc.complete, tc.total, got, tc.want)
			}
		})
	}
}
