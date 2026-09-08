package vm

import (
	"sync"
	"time"
)

// A dial that goes unanswered parks a goroutine for the life of the process, so a machine that has
// stopped answering must not be dialed again on every poll.
const (
	dialMissesBeforeHoldOff = 3
	dialHoldOff             = 30 * time.Second
)

// dialBreaker holds off dials to a machine that stopped answering.
type dialBreaker struct {
	mu      sync.Mutex
	misses  int
	nextTry time.Time
}

func (b *dialBreaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !now.Before(b.nextTry)
}

// recordMissed records a dial abandoned with no answer, and reports whether this is the miss that
// started a hold off, which is the moment worth logging.
func (b *dialBreaker) recordMissed(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.misses++
	if b.misses < dialMissesBeforeHoldOff {
		return false
	}
	first := b.misses == dialMissesBeforeHoldOff
	b.nextTry = now.Add(dialHoldOff)
	return first
}

// recordAnswered records a dial that came back, which lets dialing resume at once.
func (b *dialBreaker) recordAnswered() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.misses = 0
	b.nextTry = time.Time{}
}
