package guestexec

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"time"
)

// streamLimit is how much of one stream is held for a reader that is behind. A full stream waits for its
// reader, and stallLimit is how long it waits before the exec is cut off.
const (
	streamLimit = 8 << 20
	stallLimit  = 30 * time.Second
)

var errStreamFull = errors.New("guestexec: output was not read for too long, so the command was cut off")

// stream is one of the guest's output streams. A slow reader slows the guest down, as a terminal should,
// but a reader that stops is never waited on for long: a vsock connection the host opened and stopped
// reading can freeze the VM's whole vsock device on Apple VF.
type stream struct {
	mu         sync.Mutex
	hasData    *sync.Cond
	hasRoom    *sync.Cond
	buf        bytes.Buffer
	limit      int
	stall      time.Duration
	done       bool
	err        error
	readerGone bool
}

func newStream(limit int, stall time.Duration) *stream {
	s := &stream{limit: limit, stall: stall}
	s.hasData = sync.NewCond(&s.mu)
	s.hasRoom = sync.NewCond(&s.mu)
	return s
}

// write holds chunk for the reader, waiting while the stream is full. Output nobody will read is dropped, so
// the guest is still drained.
func (s *stream) write(chunk []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fits := s.waitForRoom(len(chunk))
	if !fits {
		return errStreamFull
	}
	if s.readerGone || s.done {
		return nil
	}
	s.buf.Write(chunk)
	s.hasData.Signal()
	return nil
}

// waitForRoom waits until size more bytes fit, the reader leaves, or the stall limit runs out. The
// caller holds s.mu.
func (s *stream) waitForRoom(size int) bool {
	if s.buf.Len()+size <= s.limit {
		return true
	}
	stalled := false
	timer := time.AfterFunc(s.stall, func() {
		s.mu.Lock()
		stalled = true
		s.mu.Unlock()
		s.hasRoom.Broadcast()
	})
	defer timer.Stop()
	for s.buf.Len()+size > s.limit && !s.readerGone && !s.done {
		if stalled {
			return false
		}
		s.hasRoom.Wait()
	}
	return true
}

// end tells the reader no more output is coming, and why when err is set.
func (s *stream) end(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		s.done, s.err = true, err
	}
	s.hasData.Broadcast()
	s.hasRoom.Broadcast()
}

func (s *stream) Read(into []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.buf.Len() == 0 && !s.done {
		s.hasData.Wait()
	}
	if s.buf.Len() > 0 {
		read, err := s.buf.Read(into)
		s.hasRoom.Signal()
		return read, err
	}
	if s.err != nil {
		return 0, s.err
	}
	return 0, io.EOF
}

// Close is the reader saying it wants no more of this stream.
func (s *stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readerGone, s.done = true, true
	s.buf.Reset()
	s.hasData.Broadcast()
	s.hasRoom.Broadcast()
	return nil
}
