// Package llmproxy carries a sandbox's LLM traffic to the gateway on the host's behalf, or to the
// provider with the user's API key, which the host adds so the key never enters the sandbox.
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

// Provider is an agent vendor's API, where a session that logs in with the user's API key is sent.
type Provider struct {
	URL string
	// KeyHeader is the header the key goes in. Authorization means a bearer token.
	KeyHeader string
}

// APIKey is the user's key for one session, and the provider it belongs to.
type APIKey struct {
	Key      string
	Provider Provider
}

// Proxy is where a session that does not use the API key sends its requests, and the headers to add to
// each one, such as a token for a gateway in front of it.
type Proxy struct {
	URL     string
	Headers http.Header
}

// errNoProxy is what a request meets when this node has no proxy address configured.
var errNoProxy = errors.New("no llm proxy is configured on this node")

// Forwarder sends a guest's requests on to the proxy.
type Forwarder struct {
	configuredProxy func() Proxy
	// apiKeyFor is the user's API key for a session that uses it, an empty key for any other.
	apiKeyFor func(sessionID string) (APIKey, error)
	client    func() *http.Client
	// onError records what the agent was told, so the same failure is visible on the host. Nil is quiet.
	onError func(error)
	// onRequest is told which session made each request, so a machine several sessions share can be
	// accounted for per session rather than as one lump. Nil is quiet.
	onRequest func(sessionID, method, path string)
	proxy     *httputil.ReverseProxy
}

// NewForwarder builds the forwarder. configuredProxy, apiKeyFor and client are read per request, since
// all three can change while a VM runs.
func NewForwarder(
	configuredProxy func() Proxy,
	apiKeyFor func(sessionID string) (APIKey, error),
	client func() *http.Client,
	onError func(error),
	onRequest func(sessionID, method, path string),
) *Forwarder {
	f := &Forwarder{
		configuredProxy: configuredProxy,
		apiKeyFor:       apiKeyFor,
		client:          client,
		onError:         onError,
		onRequest:       onRequest,
	}
	f.proxy = &httputil.ReverseProxy{
		// Rewrite, not Director: it adds no X-Forwarded-For, so the proxy learns nothing about the
		// sandbox behind this. Any that arrived are dropped for the same reason.
		Rewrite: func(r *httputil.ProxyRequest) {
			// Resolved once per request and carried on the context, so one request cannot see two
			// different addresses.
			to := routeFrom(r.In.Context())
			r.SetURL(to.url)
			r.Out.Host = to.url.Host
			// The session id is for this machine, not for the proxy or the provider, so it goes no further.
			// Neither does a key the agent sent itself.
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("X-Api-Key")
			switch {
			case to.apiKey.Key == "":
				r.Out.Header.Set("Authorization", "Bearer deepcove")
			case to.apiKey.Provider.KeyHeader == "Authorization":
				r.Out.Header.Set("Authorization", "Bearer "+to.apiKey.Key)
			default:
				r.Out.Header.Set(to.apiKey.Provider.KeyHeader, to.apiKey.Key)
			}
			// Only the proxy's route has any. Set after the placeholder, so a proxy that wants its own
			// Authorization gets it.
			for name, values := range to.headers {
				r.Out.Header[name] = values
			}
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded"} {
				r.Out.Header.Del(h)
			}
		},
		Transport:     roundTripperFunc(f.roundTrip),
		FlushInterval: -1, // answers stream, so never buffer
		// The default handler writes a bare 502 with an empty body, which reaches the agent as an
		// unexplained failure. Say which address failed and why.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			f.fail(w, fmt.Errorf("%s: %w", routeFrom(r.Context()).url, err))
		},
	}
	return f
}

// current reads the proxy configured right now, so a change in the UI reaches a running machine.
func (f *Forwarder) current() (route, error) {
	var proxy Proxy
	if f.configuredProxy != nil {
		proxy = f.configuredProxy()
	}
	if proxy.URL == "" {
		return route{}, errNoProxy
	}
	u, err := url.Parse(proxy.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return route{}, fmt.Errorf("llm proxy url %q is not usable", proxy.URL)
	}
	return route{
		url:     u,
		headers: proxy.Headers,
	}, nil
}

// roundTrip reaches the proxy over the tailnet once this node has signed in. The provider is on the
// internet, so a request with the API key always takes the ordinary network.
func (f *Forwarder) roundTrip(r *http.Request) (*http.Response, error) {
	if routeFrom(r.Context()).apiKey.Key == "" && f.client != nil {
		if c := f.client(); c != nil && c.Transport != nil {
			return c.Transport.RoundTrip(r)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// handler answers one request. No proxy configured is an error the agent can read, not a hang.
func (f *Forwarder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := sessionFrom(r.Header)
		to, err := f.route(sessionID)
		if err != nil {
			f.fail(w, err)
			return
		}
		if f.onRequest != nil {
			f.onRequest(sessionID, r.Method, r.URL.Path)
		}
		f.proxy.ServeHTTP(w, r.WithContext(withRoute(r.Context(), to)))
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

// route is where one request goes, with the API key when it goes to the provider and the proxy's
// headers when it goes to the proxy.
type route struct {
	url     *url.URL
	apiKey  APIKey
	headers http.Header
}

// route picks the provider for a session that logs in with the API key, and the proxy for any other.
func (f *Forwarder) route(sessionID string) (route, error) {
	if f.apiKeyFor != nil && sessionID != "" {
		key, err := f.apiKeyFor(sessionID)
		if err != nil {
			return route{}, err
		}
		if key.Key != "" {
			provider, err := url.Parse(key.Provider.URL)
			return route{url: provider, apiKey: key}, err
		}
	}
	return f.current()
}

// routeKey carries the resolved route from the handler to the rewrite.
type routeKey struct{}

func withRoute(ctx context.Context, to route) context.Context {
	return context.WithValue(ctx, routeKey{}, to)
}

func routeFrom(ctx context.Context) route {
	to, _ := ctx.Value(routeKey{}).(route)
	return to
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
