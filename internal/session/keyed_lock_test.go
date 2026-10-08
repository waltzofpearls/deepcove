package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyedLock_Lock(t *testing.T) {
	var locks keyedLock
	unlock, err := locks.lock(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	cases := []struct {
		name    string
		key     string
		wantErr error
	}{
		{name: "the same key waits until it gives up", key: "session-1", wantErr: context.DeadlineExceeded},
		{name: "another key does not wait", key: "session-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			other, err := locks.lock(ctx, tc.key)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil {
				other()
			}
		})
	}

	if got := locks.keys(); got != 1 {
		t.Errorf("%d keys kept, want only the held one", got)
	}
	unlock()
	if got := locks.keys(); got != 0 {
		t.Errorf("%d keys kept after the last unlock, want none", got)
	}
}

func TestKeyedLock_Lock_HandsOverToAWaiter(t *testing.T) {
	var locks keyedLock
	unlock, err := locks.lock(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		next, err := locks.lock(context.Background(), "session-1")
		if err == nil {
			next()
		}
		got <- err
	}()
	// The waiter has to be counted before the holder lets go, or the key could be dropped under it.
	for locks.users("session-1") < 2 {
		time.Sleep(time.Millisecond)
	}
	unlock()
	if err := <-got; err != nil {
		t.Fatalf("waiter: %v", err)
	}
	if n := locks.keys(); n != 0 {
		t.Errorf("%d keys kept after both let go, want none", n)
	}
}

func (l *keyedLock) users(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry := l.locks[key]; entry != nil {
		return entry.holdersAndWaiters
	}
	return 0
}

func (l *keyedLock) keys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}
