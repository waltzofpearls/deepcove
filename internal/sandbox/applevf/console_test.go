package applevf

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestConsoleBufferKeepsOutput(t *testing.T) {
	c := newConsoleBuffer()
	c.drain(strings.NewReader("[deepcove-init] starting\n[deepcove-init] ready\n"))

	got := string(c.Tail())
	if !strings.Contains(got, "starting") || !strings.Contains(got, "ready") {
		t.Errorf("Tail() = %q, want both lines", got)
	}
	if c.Truncated() {
		t.Error("a short log should not report truncation")
	}
}

// The buffer is bounded so a long lived VM cannot grow it without limit, and it keeps the NEWEST bytes
// because a boot failure is explained by what happened last.
func TestConsoleBufferKeepsTheNewestBytesWhenFull(t *testing.T) {
	c := newConsoleBuffer()
	c.drain(bytes.NewReader(bytes.Repeat([]byte("x"), consoleTailBytes*2)))
	c.append([]byte("THE-END"))

	got := string(c.Tail())
	if len(got) > consoleTailBytes {
		t.Errorf("buffer grew to %d bytes, over the %d cap", len(got), consoleTailBytes)
	}
	if !strings.HasSuffix(got, "THE-END") {
		t.Error("the most recent output must survive; it is what explains a failure")
	}
	if !c.Truncated() {
		t.Error("dropping output must be reported, not implied to be the whole log")
	}
}

// Draining is what keeps the guest from blocking on console writes once the pipe fills, so it has to
// run to completion rather than stop at the first read boundary.
func TestConsoleBufferDrainsUntilEOF(t *testing.T) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	c := newConsoleBuffer()
	go func() { c.drain(pr); close(done) }()

	for range 10 {
		if _, err := pw.Write([]byte("chunk\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = pw.Close()
	<-done

	if got := strings.Count(string(c.Tail()), "chunk"); got != 10 {
		t.Errorf("captured %d chunks, want 10", got)
	}
}

func TestConsoleBufferEmpty(t *testing.T) {
	c := newConsoleBuffer()
	if got := c.Tail(); len(got) != 0 {
		t.Errorf("a fresh buffer should be empty, got %q", got)
	}
}
