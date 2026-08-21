//go:build linux

package guestinit

import (
	"fmt"
	"io"
	"net"

	"github.com/mdlayher/vsock"
	"github.com/waltzofpearls/deepcove/internal/llmproxy"
)

// serveAgentEndpoint gives the agent a local address for its LLM calls and splices every connection to
// the host over vsock. The guest parses nothing: the host is the side that speaks to the gateway.
func serveAgentEndpoint(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", port, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go pipeToHost(conn)
		}
	}()
	return nil
}

// pipeToHost copies one agent connection to the host and back until either end hangs up.
func pipeToHost(agent net.Conn) {
	defer func() { _ = agent.Close() }()
	host, err := vsock.Dial(hostContextID, llmproxy.HostVSockPort, nil)
	if err != nil {
		logf("WARNING: LLM requests cannot reach the host: %v", err)
		return
	}
	defer func() { _ = host.Close() }()

	// The agent closes first when it is done, which ends the upstream copy and then the downstream one.
	go func() {
		_, _ = io.Copy(host, agent)
		_ = host.Close()
	}()
	_, _ = io.Copy(agent, host)
}
