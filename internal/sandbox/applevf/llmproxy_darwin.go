//go:build darwin

package applevf

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
)

// serveLLMForward answers this VM's LLM requests. One listener per VM, like the other guest services.
//
// Every VM gets one, whether or not a proxy is configured right now: the address is read per request, so
// setting one later reaches a machine that is already running.
func serveLLMForward(m *machine, proxyURL func() string, client func() *http.Client, logger *slog.Logger) error {
	forwarder := llmproxy.NewForwarder(proxyURL, client, func(err error) {
		logger.Warn("llm proxy request failed", "err", err)
	}, func(sessionID, method, path string) {
		logger.Info("llm proxy request", "session", sessionID, "method", method, "path", path)
	})
	device, err := m.socketDevice()
	if err != nil {
		return err
	}
	listener, err := device.Listen(llmproxy.HostVSockPort)
	if err != nil {
		return fmt.Errorf("applevf: listen for LLM requests on vsock port %d: %w", llmproxy.HostVSockPort, err)
	}
	m.llmListener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // the listener closed with the VM
			}
			go func() {
				// A forwarding failure is the agent's whole problem, so it is worth a line here as well
				// as the error the agent sees.
				if err := forwarder.Serve(conn); err != nil && !errors.Is(err, net.ErrClosed) {
					logger.Warn("llm proxy forwarding stopped", "err", err)
				}
			}()
		}
	}()
	return nil
}
