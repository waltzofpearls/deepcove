package sharedlogin

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"time"
)

// Port is the vsock port the host serves the shared login on. The guest dials it.
const Port uint32 = 1026

// maxMessageSize stops a corrupt or hostile length from driving a big allocation. A login is a few KB.
const maxMessageSize = 64 << 10

// answerTimeout bounds one exchange. A refresh is the slow part.
const answerTimeout = 30 * time.Second

// Request is a session asking for the shared login, or offering one it made itself.
type Request struct {
	// Agent is whose login this is. A machine that several sessions share runs more than one agent.
	Agent string `json:"agent"`
	// Offer is nil to just ask.
	Offer *Login `json:"offer,omitempty"`
}

// Reply is the host's answer: the current login without its refresh token, or why there is none.
type Reply struct {
	Login            *Login `json:"login,omitempty"`
	NoLogin          bool   `json:"no_login,omitempty"`
	DifferentAccount bool   `json:"different_account,omitempty"`
	NotSharing       bool   `json:"not_sharing,omitempty"`
	Error            string `json:"error,omitempty"`
}

// Serve answers one request on conn, from session sessionID on the VM sandboxID. agents are the only
// ones whose login this VM may have, so a session's machine never gets another agent's login.
func Serve(conn net.Conn, keeper *Keeper, agents []string, sessionID, sandboxID string) error {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), answerTimeout)
	defer cancel()
	if err := conn.SetDeadline(time.Now().Add(answerTimeout)); err != nil {
		return err
	}
	request, err := readMessage[Request](conn)
	if err != nil {
		return err
	}
	if !slices.Contains(agents, request.Agent) {
		return writeMessage(conn, Reply{Error: fmt.Sprintf("this machine gets no shared login for %q", request.Agent)})
	}
	return writeMessage(conn, keeper.answer(ctx, request.Agent, sessionID, sandboxID, request))
}

// Ask sends one request on conn and returns the host's reply.
func Ask(conn net.Conn, request Request) (Reply, error) {
	if err := writeMessage(conn, request); err != nil {
		return Reply{}, err
	}
	return readMessage[Reply](conn)
}

// answer takes an offer first, so a session that just offered its login gets the copy to keep.
func (k *Keeper) answer(ctx context.Context, agent, sessionID, sandboxID string, request Request) Reply {
	if k.isSharing != nil {
		sharing, err := k.isSharing(agent, sessionID)
		if err != nil {
			return Reply{Error: err.Error()}
		}
		if !sharing {
			return Reply{NotSharing: true}
		}
	}
	if request.Offer != nil {
		err := k.Offer(ctx, agent, sandboxID, *request.Offer)
		if errors.Is(err, ErrDifferentAccount) {
			return Reply{DifferentAccount: true}
		}
		if err != nil {
			return Reply{Error: err.Error()}
		}
	}
	login, err := k.ForSession(ctx, agent)
	if errors.Is(err, ErrNoLogin) {
		return Reply{NoLogin: true}
	}
	if err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{Login: &login}
}

func writeMessage[T any](w io.Writer, msg T) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("sharedlogin: encode: %w", err)
	}
	if len(body) > maxMessageSize {
		return fmt.Errorf("sharedlogin: message is %d bytes, over the %d limit", len(body), maxMessageSize)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := w.Write(append(header[:], body...)); err != nil {
		return fmt.Errorf("sharedlogin: write: %w", err)
	}
	return nil
}

func readMessage[T any](r io.Reader) (T, error) {
	var zero T
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return zero, fmt.Errorf("sharedlogin: read header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxMessageSize {
		return zero, fmt.Errorf("sharedlogin: message is %d bytes, over the %d limit", size, maxMessageSize)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return zero, fmt.Errorf("sharedlogin: read body: %w", err)
	}
	var msg T
	if err := json.Unmarshal(body, &msg); err != nil {
		return zero, fmt.Errorf("sharedlogin: decode: %w", err)
	}
	return msg, nil
}
