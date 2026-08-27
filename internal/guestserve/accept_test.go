package guestserve

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeConn struct{ net.Conn }

func (c *fakeConn) Close() error { return nil }

// scriptedListener returns the queued results in order, then blocks until it is closed.
type scriptedListener struct {
	mu      sync.Mutex
	results []error
	closed  chan struct{}
	once    sync.Once
}

func newScriptedListener(results ...error) *scriptedListener {
	return &scriptedListener{results: results, closed: make(chan struct{})}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.results) > 0 {
		err := l.results[0]
		l.results = l.results[1:]
		l.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &fakeConn{}, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *scriptedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *scriptedListener) Addr() net.Addr { return nil }

func TestAccept(t *testing.T) {
	cases := []struct {
		name    string
		results []error
		serve   func(net.Conn)
		want    int
	}{
		{name: "one connection", results: []error{nil}, want: 1},
		{
			name:    "an accept failure does not end the loop",
			results: []error{errors.New("too many open files"), nil, nil},
			want:    2,
		},
		{
			name:    "a panicking connection does not end the loop",
			results: []error{nil, nil},
			serve:   func(net.Conn) { panic("boom") },
			want:    2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listener := newScriptedListener(tc.results...)
			served := make(chan struct{}, len(tc.results))
			serve := func(conn net.Conn) {
				if tc.serve != nil {
					defer func() { served <- struct{}{} }()
					tc.serve(conn)
					return
				}
				served <- struct{}{}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Accept(ctx, listener, func(string, ...any) {}, serve) }()

			for i := 0; i < tc.want; i++ {
				select {
				case <-served:
				case <-time.After(5 * time.Second):
					t.Fatalf("got %d connections, want %d", i, tc.want)
				}
			}

			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Accept returned %v, want nil on shutdown", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Accept did not return after the context ended")
			}
		})
	}
}
