package applevf

import (
	"io"
	"sync"
)

// consoleTailBytes is how much recent console output to keep. Enough to hold the kernel boot messages
// and the guest init's progress lines, which is what makes a failed boot diagnosable.
const consoleTailBytes = 64 << 10

// consoleBuffer keeps the most recent output from a guest's serial console.
//
// It exists for two reasons. The console is an OS pipe, so if nobody reads it the guest eventually
// BLOCKS on a console write once the pipe buffer fills; draining it keeps the VM running. And when a
// guest fails to come up, the tail is the only evidence of why, so a readiness failure can say what the
// guest was doing instead of just reporting a timeout.
type consoleBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func newConsoleBuffer() *consoleBuffer {
	return &consoleBuffer{buf: make([]byte, 0, consoleTailBytes)}
}

// drain reads r until it ends, keeping the tail. Runs for the life of the VM.
func (c *consoleBuffer) drain(r io.Reader) {
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			c.append(chunk[:n])
		}
		if err != nil {
			return
		}
	}
}

func (c *consoleBuffer) append(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.buf = append(c.buf, p...)
	if len(c.buf) > consoleTailBytes {
		// Keep the newest bytes: a boot failure is explained by what happened last, not first.
		c.buf = c.buf[len(c.buf)-consoleTailBytes:]
	}
}

// Tail returns a copy of the buffered output.
func (c *consoleBuffer) Tail() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	return out
}
