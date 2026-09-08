package vm

import (
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/credbroker"
	"github.com/waltzofpearls/deepcove/internal/guestcfg"
	"github.com/waltzofpearls/deepcove/internal/llmproxy"
)

// ServeCredentials runs the host side credential broker for this VM: the guest dials in at boot to fetch
// its stored login and again at shutdown to hand back whatever the agent refreshed.
//
// Unlike exec and control, the host listens here, because the guest is the side that needs something.
func (g *Guest) ServeCredentials(store credbroker.Store) error {
	return g.serve("credential broker", credbroker.Port, func(conn net.Conn) {
		_ = credbroker.Serve(conn, store)
	})
}

// ServeConfig answers this VM's boot configuration request. One listener per VM, so a connection already
// identifies the session and the guest never has to name itself.
func (g *Guest) ServeConfig(cfg guestcfg.Config) error {
	return g.serve("config service", guestcfg.Port, func(conn net.Conn) {
		_ = guestcfg.Serve(conn, cfg)
	})
}

// ServeLLMForward answers this VM's LLM requests.
//
// Every VM gets one, whether or not a proxy is configured right now: the address is read per request, so
// setting one later reaches a machine that is already running.
func (g *Guest) ServeLLMForward(proxyURL func() string, client func() *http.Client) error {
	forwarder := llmproxy.NewForwarder(proxyURL, client, func(err error) {
		g.logger.Warn("llm proxy request failed", "err", err)
	}, func(sessionID, method, path string) {
		g.logger.Info("llm proxy request", "session", sessionID, "method", method, "path", path)
	})
	return g.serve("llm forwarder", llmproxy.HostVSockPort, func(conn net.Conn) {
		// A forwarding failure is the agent's whole problem, so it is worth a line here as well as the
		// error the agent sees.
		if err := forwarder.Serve(conn); err != nil && !errors.Is(err, net.ErrClosed) {
			g.logger.Warn("llm proxy forwarding stopped", "err", err)
		}
	})
}

// serve listens on a vsock port the guest dials in on and runs handler for each connection, until the
// guest is stopped.
func (g *Guest) serve(name string, port uint32, handler func(net.Conn)) error {
	listener, err := g.machine.Listen(port)
	if err != nil {
		return fmt.Errorf("vm: listen for the guest %s on vsock port %d: %w", name, port, err)
	}
	g.keep(listener)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // the listener closed with the guest
			}
			go func() {
				// A panic in one connection must not take deepcove down with it.
				defer func() {
					if r := recover(); r != nil {
						g.logger.Error("vm: "+name+" panicked", "err", r)
					}
				}()
				handler(conn)
			}()
		}
	}()
	return nil
}
