// Package llmproxy carries a sandbox's LLM traffic to the gateway on the host's behalf.
//
// The sandbox cannot call the gateway itself: it lives on the tailnet, which netguard blackholes. So the
// guest hands over the whole connection on vsock and the host makes the call, which also gives the
// request this node's identity. Bodies are never read; they hold the prompt and the answer.
package llmproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HostVSockPort is the vsock port the host serves the LLM hop on.
const HostVSockPort uint32 = 1028

// LLMRequestPort is where the sandbox sends its LLM requests, plain TCP inside the guest.
const LLMRequestPort = 9100

// AuthTokenPrefix is what deepcove puts in front of a session's id in the Authorization header it gives
// the agent. The proxy holds the real credential, so this value is ours to use: it is how a request
// arriving on a shared machine says which session made it.
//
// It attributes, it does not authenticate. Anything inside the machine can read a sibling's environment,
// so a determined process could send another session's id. Identity that holds up would have to come
// from the connection, one port per session, rather than from the request.
const AuthTokenPrefix = "deepcove-"

// AuthToken is the value one session's agent sends.
func AuthToken(sessionID string) string { return AuthTokenPrefix + sessionID }

// sessionFrom reads the session id back out, or "" when nothing recognisable is there.
func sessionFrom(h http.Header) string {
	v := strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(v, AuthTokenPrefix) {
		return ""
	}
	return strings.TrimPrefix(v, AuthTokenPrefix)
}

// LLMEndpoint is what a sandbox uses instead of the provider's URL.
func LLMEndpoint() string { return fmt.Sprintf("http://127.0.0.1:%d", LLMRequestPort) }

// errNoProxy is what a request meets when this node has no proxy address configured.
var errNoProxy = errors.New("no llm proxy is configured on this node")

// Forwarder sends a guest's requests on to the proxy.
type Forwarder struct {
	proxyURL func() string
	client   func() *http.Client
	// onError records what the agent was told, so the same failure is visible on the host. Nil is quiet.
	onError func(error)
	// onRequest is told which session made each request, so a machine several sessions share can be
	// accounted for per session rather than as one lump. Nil is quiet.
	onRequest func(sessionID, method, path string)
	proxy     *httputil.ReverseProxy
}

// NewForwarder builds the forwarder. proxyURL and client are read per request rather than kept: the
// proxy's address changes in the UI, and the tailnet client only exists once this node has signed in.
func NewForwarder(
	proxyURL func() string,
	client func() *http.Client,
	onError func(error),
	onRequest func(sessionID, method, path string),
) *Forwarder {
	f := &Forwarder{proxyURL: proxyURL, client: client, onError: onError, onRequest: onRequest}
	f.proxy = &httputil.ReverseProxy{
		// Rewrite, not Director: it adds no X-Forwarded-For, so the proxy learns nothing about the
		// sandbox behind this. Any that arrived are dropped for the same reason.
		Rewrite: func(r *httputil.ProxyRequest) {
			// Resolved once per request and carried on the context, so one request cannot see two
			// different addresses.
			u := upstreamFrom(r.In.Context())
			r.SetURL(u)
			r.Out.Host = u.Host
			// The session id is for this machine, not for the proxy, so it goes no further.
			r.Out.Header.Set("Authorization", "Bearer deepcove")
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded"} {
				r.Out.Header.Del(h)
			}
		},
		Transport:     roundTripperFunc(f.roundTrip),
		FlushInterval: -1, // answers stream, so never buffer
		// The default handler writes a bare 502 with an empty body, which reaches the agent as an
		// unexplained failure. Say which address failed and why.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			f.fail(w, fmt.Errorf("%s: %w", upstreamFrom(r.Context()), err))
		},
	}
	return f
}

// current parses the address configured right now, so a change in the UI reaches a running machine.
func (f *Forwarder) current() (*url.URL, error) {
	raw := ""
	if f.proxyURL != nil {
		raw = f.proxyURL()
	}
	if raw == "" {
		return nil, errNoProxy
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("llm proxy url %q is not usable", raw)
	}
	return u, nil
}

// roundTrip sends the request the way this node can reach the proxy: over the tailnet when this node has
// signed in, and over its ordinary network when it has not.
func (f *Forwarder) roundTrip(r *http.Request) (*http.Response, error) {
	if f.client != nil {
		if c := f.client(); c != nil && c.Transport != nil {
			return c.Transport.RoundTrip(r)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// handler answers one request. No proxy configured is an error the agent can read, not a hang.
func (f *Forwarder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := f.current()
		if err != nil {
			f.fail(w, err)
			return
		}
		if f.onRequest != nil {
			f.onRequest(sessionFrom(r.Header), r.Method, r.URL.Path)
		}
		f.proxy.ServeHTTP(w, r.WithContext(withUpstream(r.Context(), u)))
	})
}

// fail answers the agent and tells the host the same thing.
func (f *Forwarder) fail(w http.ResponseWriter, err error) {
	if f.onError != nil {
		f.onError(err)
	}
	writeProxyError(w, err)
}

// writeProxyError answers in the shape an agent expects, so the reason lands in its own error line
// instead of looking like a dead connection.
func writeProxyError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    "deepcove_llm_proxy_error",
			"message": "deepcove could not forward this request: " + err.Error(),
		},
	})
}

// upstreamKey carries the resolved address from the handler to the rewrite.
type upstreamKey struct{}

func withUpstream(ctx context.Context, u *url.URL) context.Context {
	return context.WithValue(ctx, upstreamKey{}, u)
}

func upstreamFrom(ctx context.Context) *url.URL {
	u, _ := ctx.Value(upstreamKey{}).(*url.URL)
	return u
}

// roundTripperFunc lets a method stand in as a Transport.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

// Serve reads one vsock connection as HTTP until the guest hangs up. Keep-alive means several requests
// can share it, which is why this is a server and not one round trip.
func (f *Forwarder) Serve(conn net.Conn) error {
	srv := &http.Server{
		Handler:           f.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: an answer streams for minutes and a deadline would cut it off.
	}
	return srv.Serve(newConnectionListener(conn))
}

// connectionListener turns one existing connection into a listener, which is what lets http.Server read
// a vsock stream. Accept hands it over once, then waits, so Serve stays up while the request is in
// flight instead of returning immediately.
type connectionListener struct {
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
}

func newConnectionListener(conn net.Conn) *connectionListener {
	return &connectionListener{conn: conn, closed: make(chan struct{})}
}

func (l *connectionListener) Accept() (net.Conn, error) {
	var first net.Conn
	l.once.Do(func() { first = &closeNotifier{Conn: l.conn, listener: l} })
	if first != nil {
		return first, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *connectionListener) Close() error   { return nil } // the connection closes itself
func (l *connectionListener) Addr() net.Addr { return l.conn.LocalAddr() }

// closeNotifier tells the listener when the server is done with the connection.
type closeNotifier struct {
	net.Conn
	listener *connectionListener
	once     sync.Once
}

func (c *closeNotifier) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.listener.closed) })
	return err
}
