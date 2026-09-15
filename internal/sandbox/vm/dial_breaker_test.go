package vm

import (
	"testing"
	"time"
)

func TestDialBreaker(t *testing.T) {
	start := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	missTimes := func(b *dialBreaker, n int, at time.Time) {
		for range n {
			b.recordMissed(at)
		}
	}

	cases := []struct {
		name  string
		setup func(b *dialBreaker)
		at    time.Time
		want  bool
	}{
		{name: "a machine that has answered is dialed", setup: func(*dialBreaker) {}, at: start, want: true},
		{name: "one miss is not enough to hold off", setup: func(b *dialBreaker) { missTimes(b, 1, start) }, at: start, want: true},
		{name: "three misses hold off", setup: func(b *dialBreaker) { missTimes(b, 3, start) }, at: start, want: false},
		{name: "the hold off ends", setup: func(b *dialBreaker) { missTimes(b, 3, start) }, at: start.Add(dialHoldOff), want: true},
		{
			name:  "an answer clears the misses",
			setup: func(b *dialBreaker) { missTimes(b, 3, start); b.recordAnswered() },
			at:    start,
			want:  true,
		},
		{
			name: "a miss after the hold off pushes it out again",
			setup: func(b *dialBreaker) {
				missTimes(b, 3, start)
				b.recordMissed(start.Add(dialHoldOff))
			},
			at:   start.Add(dialHoldOff),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var breaker dialBreaker
			tc.setup(&breaker)
			if got := breaker.allow(tc.at); got != tc.want {
				t.Errorf("allow = %v, want %v", got, tc.want)
			}
		})
	}
}
