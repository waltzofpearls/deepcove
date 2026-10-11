package session

import (
	"context"
	"sync"
)

// keyedLock is one lock per key. A key is forgotten once nothing holds it or waits for it.
type keyedLock struct {
	mu    sync.Mutex
	locks map[string]*keyedLockEntry
}

type keyedLockEntry struct {
	held              chan struct{}
	holdersAndWaiters int
}

func (l *keyedLock) lock(ctx context.Context, key string) (unlock func(), err error) {
	// Checked first: select picks at random when the lock is free too.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*keyedLockEntry{}
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &keyedLockEntry{held: make(chan struct{}, 1)}
		l.locks[key] = entry
	}
	entry.holdersAndWaiters++
	l.mu.Unlock()

	select {
	case entry.held <- struct{}{}:
		return func() {
			<-entry.held
			l.leave(key, entry)
		}, nil
	case <-ctx.Done():
		l.leave(key, entry)
		return nil, ctx.Err()
	}
}

func (l *keyedLock) leave(key string, entry *keyedLockEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.holdersAndWaiters--
	if entry.holdersAndWaiters == 0 {
		delete(l.locks, key)
	}
}
