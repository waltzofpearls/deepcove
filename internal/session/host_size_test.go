package session

import "testing"

func TestRoundToGiB(t *testing.T) {
	cases := []struct {
		name string
		mb   int64
		want int64
	}{
		{"zero becomes one GiB", 0, 1024},
		{"below one GiB becomes one", 500, 1024},
		{"already whole", 1024, 1024},
		{"rounds up", 1536, 2048},
		{"rounds down", 1500, 1024},
		{"120.56 GiB becomes 121", 123456, 121 * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roundToGiB(tc.mb); got != tc.want {
				t.Errorf("roundToGiB(%d) = %d, want %d", tc.mb, got, tc.want)
			}
		})
	}
}

func TestUsableCoresIsPositive(t *testing.T) {
	// Every default is derived from this, so a zero would size a VM at the minimum on every machine.
	if got := usableCores(); got < 1 {
		t.Errorf("usableCores = %d, want at least 1", got)
	}
}
