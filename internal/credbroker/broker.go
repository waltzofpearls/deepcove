// Package credbroker moves agent credentials between the host and a session VM without ever writing them
// to a credential shaped path on the host filesystem.
//
// The Docker runtime shares one login across sessions with a named volume, which cannot port to VMs: two
// running VMs cannot mount one disk image read write without corrupting it. So the host brokers instead.
// At boot the guest fetches the stored credentials into a tmpfs, and at shutdown it hands back whatever
// the agent refreshed, which the host folds into its own opaque store.
//
// The direction is the opposite of the exec and control services: here the HOST listens on vsock and the
// guest dials it, because the guest is the one that needs something at boot.
//
// Only the personal profile uses this. A work profile that relies on gateway injected credentials asks
// for nothing and stores nothing, so those credentials never enter the VM at all.
package credbroker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

// Port is the vsock port the host's credential broker listens on.
const Port uint32 = 1026

// maxBundleSize bounds a credential bundle so a corrupt length cannot drive an unbounded allocation.
const maxBundleSize = 1 << 20

// Op is what the guest wants.
type Op string

const (
	// OpFetch asks the host for the stored credentials, at guest boot.
	OpFetch Op = "fetch"
	// OpStore hands refreshed credentials back, at guest shutdown.
	OpStore Op = "store"
)

// Request is the guest's message. Files carries the bundle for OpStore and is empty for OpFetch.
type Request struct {
	Op    Op                `json:"op"`
	Agent string            `json:"agent"`
	Files map[string][]byte `json:"files,omitempty"`
}

// Response is the host's answer. Files carries the bundle for OpFetch.
type Response struct {
	Files map[string][]byte `json:"files,omitempty"`
	Error string            `json:"error,omitempty"`
}

// Store is where the host keeps credentials. Implementations must not write them anywhere a scanner
// would recognize as credentials: an opaque blob inside deepcove's own data store, ideally encrypted.
type Store interface {
	// Load returns the stored bundle for an agent, or an empty map when nothing is stored yet, which is
	// the normal first run case rather than an error.
	Load(agent string) (map[string][]byte, error)
	// Save replaces the stored bundle for an agent.
	Save(agent string, files map[string][]byte) error
}

// ErrNoCredentials means the guest asked for credentials that were never stored. The guest treats this as
// "the agent has not logged in yet" and carries on.
var ErrNoCredentials = errors.New("credbroker: no credentials stored for this agent")

// Serve handles one guest request against the store.
func Serve(conn net.Conn, store Store) error {
	defer func() { _ = conn.Close() }()

	req, err := readMessage[Request](conn)
	if err != nil {
		return err
	}
	switch req.Op {
	case OpFetch:
		files, err := store.Load(req.Agent)
		if err != nil {
			return writeMessage(conn, Response{Error: err.Error()})
		}
		return writeMessage(conn, Response{Files: files})
	case OpStore:
		if err := store.Save(req.Agent, req.Files); err != nil {
			return writeMessage(conn, Response{Error: err.Error()})
		}
		return writeMessage(conn, Response{})
	default:
		return writeMessage(conn, Response{Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

// Fetch asks the host for an agent's stored credentials.
func Fetch(conn net.Conn, agent string) (map[string][]byte, error) {
	if err := writeMessage(conn, Request{Op: OpFetch, Agent: agent}); err != nil {
		return nil, err
	}
	resp, err := readMessage[Response](conn)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("credbroker: host refused: %s", resp.Error)
	}
	if len(resp.Files) == 0 {
		return nil, ErrNoCredentials
	}
	return resp.Files, nil
}

// Store hands refreshed credentials back to the host.
func StoreFiles(conn net.Conn, agent string, files map[string][]byte) error {
	if err := writeMessage(conn, Request{Op: OpStore, Agent: agent, Files: files}); err != nil {
		return err
	}
	resp, err := readMessage[Response](conn)
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("credbroker: host refused: %s", resp.Error)
	}
	return nil
}

// writeMessage and readMessage frame one JSON value with a length prefix, the same shape guestexec uses
// for its opening request.
func writeMessage[T any](w io.Writer, msg T) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("credbroker: encode: %w", err)
	}
	if len(body) > maxBundleSize {
		return fmt.Errorf("credbroker: message is %d bytes, over the %d limit", len(body), maxBundleSize)
	}
	var hdr [4]byte
	putUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("credbroker: write header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("credbroker: write body: %w", err)
	}
	return nil
}

func readMessage[T any](r io.Reader) (T, error) {
	var zero T
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return zero, fmt.Errorf("credbroker: read header: %w", err)
	}
	size := getUint32(hdr[:])
	if size > maxBundleSize {
		return zero, fmt.Errorf("credbroker: message is %d bytes, over the %d limit", size, maxBundleSize)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return zero, fmt.Errorf("credbroker: read body: %w", err)
	}
	var msg T
	if err := json.Unmarshal(body, &msg); err != nil {
		return zero, fmt.Errorf("credbroker: decode: %w", err)
	}
	return msg, nil
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func getUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
