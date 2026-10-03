package guestinit

import (
	"testing"
	"time"
)

func TestParseHardwareClock(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Time
		wantErr bool
	}{
		{"seconds with a newline", "1791019785\n", time.Unix(1791019785, 0), false},
		{"empty", "", time.Time{}, true},
		{"not a number", "soon\n", time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHardwareClock([]byte(tc.raw))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClockStepNeeded(t *testing.T) {
	hardware := time.Unix(1791019785, 0)
	cases := []struct {
		name  string
		guest time.Time
		want  bool
	}{
		{"same", hardware, false},
		{"under a second of rounding", hardware.Add(-900 * time.Millisecond), false},
		{"behind after a sleep", hardware.Add(-47 * time.Second), true},
		{"ahead", hardware.Add(3 * time.Second), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clockStepNeeded(tc.guest, hardware); got != tc.want {
				t.Errorf("clockStepNeeded = %v, want %v", got, tc.want)
			}
		})
	}
}
