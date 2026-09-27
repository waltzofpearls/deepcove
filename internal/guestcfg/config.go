// Package guestcfg hands a session VM its per boot configuration: the entrypoint the guest should run,
// and which agent's credentials it brokers.
//
// This exists because the base image is cached per image digest and shared by every session using that
// image, while the entrypoint is per session (it carries the session name, the setup script, and the
// agent launch command). Baking it into the image would either break the cache or give every session the
// same command, so the guest asks for it instead.
//
// Like credbroker, the HOST listens and the guest dials, because the guest is the side that needs
// something at boot. The host runs one listener per VM, so a connection already identifies the session
// and the guest never has to name itself.
package guestcfg

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

// Port is the vsock port the host serves guest configuration on.
const Port uint32 = 1027

// maxMessageSize bounds a message so a corrupt length cannot drive an unbounded allocation. The
// entrypoint is a shell script, so this is generous rather than tight.
const maxMessageSize = 1 << 20

// Config is what a guest needs that its image cannot know.
type Config struct {
	// Entrypoint is the argv the guest runs to start the session. It performs the agent setup and writes
	// the agent's launch script.
	Entrypoint []string `json:"entrypoint"`
	// Agent names the credential bundle this session brokers with the host. Empty brokers nothing.
	Agent string `json:"agent"`
	// Env is the session environment, merged from the agent registry and the create request.
	Env map[string]string `json:"env,omitempty"`
	// OverlayBytes caps the session's writable layer. Zero leaves it uncapped, which is tmpfs's default
	// of half the guest's RAM. Worth setting: an uncapped overlay lets a runaway write consume the
	// guest's memory, because the writable layer IS memory today.
	OverlayBytes int64 `json:"overlay_bytes,omitempty"`
	// Mounts are host directories shared into this VM over virtio-fs. The guest mounts each by tag.
	Mounts []Mount `json:"mounts,omitempty"`
	// HomeDevice is the block device holding the agent's home, kept across restarts so a session resumes
	// where it left off. Empty means this VM has no persistent home and everything lives in the overlay.
	//
	// The host attaches it raw. Whoever finds it unformatted formats it, which is the entrypoint rather
	// than the guest init: formatting needs tools the base image does not ship, and the entrypoint is
	// already installing packages at that point.
	HomeDevice string `json:"home_device,omitempty"`
	// SwapDevice is a raw block device the guest turns into swap, so the writable layer can spill to
	// disk instead of the kernel killing the agent. Empty means no swap, which is the older behaviour.
	SwapDevice string `json:"swap_device,omitempty"`
	// NetworkAllow are addresses the sandbox may reach despite the private network block, as an IP or a
	// CIDR. Set only from the host's command line, never from the API or the database.
	NetworkAllow []string `json:"network_allow,omitempty"`
	// LLMRequestPort is the local port the guest offers for LLM requests, spliced to the host over
	// vsock. Zero means the session calls the provider itself.
	LLMRequestPort int `json:"llm_request_port,omitempty"`
}

// Mount is one virtio-fs share: the tag the host announced, and where the guest should mount it.
type Mount struct {
	Tag      string `json:"tag"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Serve answers one configuration request.
func Serve(conn net.Conn, cfg Config) error {
	defer func() { _ = conn.Close() }()
	return writeMessage(conn, cfg)
}

// Fetch asks the host for this VM's configuration.
func Fetch(conn net.Conn) (Config, error) {
	return readMessage[Config](conn)
}

func writeMessage[T any](w io.Writer, msg T) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("guestcfg: encode: %w", err)
	}
	if len(body) > maxMessageSize {
		return fmt.Errorf("guestcfg: message is %d bytes, over the %d limit", len(body), maxMessageSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("guestcfg: write header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("guestcfg: write body: %w", err)
	}
	return nil
}

func readMessage[T any](r io.Reader) (T, error) {
	var zero T
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return zero, fmt.Errorf("guestcfg: read header: %w", err)
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxMessageSize {
		return zero, fmt.Errorf("guestcfg: message is %d bytes, over the %d limit", size, maxMessageSize)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return zero, fmt.Errorf("guestcfg: read body: %w", err)
	}
	var msg T
	if err := json.Unmarshal(body, &msg); err != nil {
		return zero, fmt.Errorf("guestcfg: decode: %w", err)
	}
	return msg, nil
}
