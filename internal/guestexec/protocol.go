// Package guestexec carries command execution between the host and a session VM over vsock. The host
// dials the guest, sends an ExecRequest, and then both ends speak the same small frame protocol for
// stdio, terminal resizes, signals, and the final exit status.
//
// Both the client and the server operate on a plain net.Conn, so the whole protocol can be exercised
// host against guest over net.Pipe in a unit test on any OS. Only the transport (vsock dialing on the
// host, the listener and PTY in the guest) needs platform specific code.
package guestexec

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Port is the vsock port the guest's exec server listens on. Ports below 1024 are privileged in the
// guest, so this sits just above that range.
const Port uint32 = 1024

// maxFrameSize bounds a single frame's payload so a corrupt or hostile length prefix cannot make the
// reader allocate without limit. Comfortably larger than any terminal write.
const maxFrameSize = 1 << 20

// maxRequestSize bounds the JSON exec request for the same reason.
const maxRequestSize = 1 << 16

// FrameType tags what a frame carries. Stdin, Resize, and Signal flow host to guest; Stdout, Stderr, and
// Exit flow guest to host.
type FrameType byte

const (
	FrameStdin FrameType = iota + 1
	FrameStdout
	FrameStderr
	FrameResize
	FrameSignal
	FrameExit
	// FrameStdinClose tells the guest the host will send no more stdin, so the child sees EOF. A zero
	// length Stdin frame cannot mean this, because it is indistinguishable from an empty write.
	FrameStdinClose
)

func (t FrameType) String() string {
	switch t {
	case FrameStdin:
		return "stdin"
	case FrameStdout:
		return "stdout"
	case FrameStderr:
		return "stderr"
	case FrameResize:
		return "resize"
	case FrameSignal:
		return "signal"
	case FrameExit:
		return "exit"
	case FrameStdinClose:
		return "stdin-close"
	default:
		return fmt.Sprintf("frame(%d)", byte(t))
	}
}

// ErrFrameTooLarge means a peer announced a payload beyond maxFrameSize, which indicates a desynced or
// corrupt stream rather than a recoverable condition.
var ErrFrameTooLarge = errors.New("guestexec: frame exceeds maximum size")

// ExecRequest is the opening message: what to run and how. It is sent once, before any frame.
type ExecRequest struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
	Dir  string            `json:"dir,omitempty"`
	// Tty allocates a pseudo terminal, which merges stderr into stdout the way a real terminal does and
	// makes Resize meaningful. Required for attaching to the agent's tmux session.
	Tty  bool   `json:"tty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// WriteRequest sends the opening request, length prefixed so the guest knows where the JSON ends and the
// frame stream begins.
func WriteRequest(w io.Writer, req ExecRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("guestexec: encode request: %w", err)
	}
	if len(body) > maxRequestSize {
		return ErrFrameTooLarge
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("guestexec: write request header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("guestexec: write request: %w", err)
	}
	return nil
}

// ReadRequest reads the opening request written by WriteRequest.
func ReadRequest(r io.Reader) (ExecRequest, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return ExecRequest{}, fmt.Errorf("guestexec: read request header: %w", err)
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxRequestSize {
		return ExecRequest{}, ErrFrameTooLarge
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return ExecRequest{}, fmt.Errorf("guestexec: read request: %w", err)
	}
	var req ExecRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ExecRequest{}, fmt.Errorf("guestexec: decode request: %w", err)
	}
	return req, nil
}

// WriteFrame writes one framed message: a type byte, a big endian length, then the payload.
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > maxFrameSize {
		return ErrFrameTooLarge
	}
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("guestexec: write frame header: %w", err)
	}
	if len(payload) == 0 {
		return nil
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("guestexec: write frame payload: %w", err)
	}
	return nil
}

// ReadFrame reads one framed message. It returns io.EOF cleanly when the peer closed between frames.
func ReadFrame(r io.Reader) (FrameType, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err // io.EOF here means a clean close between frames
	}
	size := binary.BigEndian.Uint32(hdr[1:])
	if size > maxFrameSize {
		return 0, nil, ErrFrameTooLarge
	}
	if size == 0 {
		return FrameType(hdr[0]), nil, nil
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("guestexec: read frame payload: %w", err)
	}
	return FrameType(hdr[0]), payload, nil
}

// EncodeResize and DecodeResize carry terminal dimensions in a fixed four byte payload.
func EncodeResize(cols, rows uint16) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:], cols)
	binary.BigEndian.PutUint16(b[2:], rows)
	return b[:]
}

func DecodeResize(payload []byte) (cols, rows uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, fmt.Errorf("guestexec: resize payload is %d bytes, want 4", len(payload))
	}
	return binary.BigEndian.Uint16(payload[0:]), binary.BigEndian.Uint16(payload[2:]), nil
}

// EncodeExit and DecodeExit carry the child's exit status.
func EncodeExit(code int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(code))
	return b[:]
}

func DecodeExit(payload []byte) (int32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("guestexec: exit payload is %d bytes, want 4", len(payload))
	}
	return int32(binary.BigEndian.Uint32(payload)), nil
}

// EncodeSignal and DecodeSignal carry a Unix signal number.
func EncodeSignal(sig int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(sig))
	return b[:]
}

func DecodeSignal(payload []byte) (int32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("guestexec: signal payload is %d bytes, want 4", len(payload))
	}
	return int32(binary.BigEndian.Uint32(payload)), nil
}
