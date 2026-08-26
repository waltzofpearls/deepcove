// aperture-relay forwards HTTP to an upstream from the machine it runs on.
//
// The point is where the connection comes from. Aperture identifies a caller by the source address and
// asks Tailscale who owns it, so a request relayed here arrives as this machine rather than as whoever
// called the relay. Run it on the machine whose identity the requests should carry. Everything else in
// this file is hygiene around that one job.
//
// It deliberately never reads a request or response body. Those hold the prompt and the answer.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aperture-relay:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("aperture-relay", flag.ContinueOnError)
	// Localhost by default. This machine may have a public interface, and a relay to an AI gateway is
	// not something to expose by accident, so reaching it from elsewhere is an explicit choice.
	listen := fs.String("listen", "127.0.0.1:9001",
		"address to listen on; set the tailnet address to be reachable from other machines")
	upstream := fs.String("upstream", "", "host or host:port to forward to (required)")
	var allow repeatedFlag
	fs.Var(&allow, "allow", "IP or CIDR allowed to connect (repeatable)")
	allowAny := fs.Bool("allow-any", false,
		"accept any source; only when something else already restricts who can reach this port")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *upstream == "" {
		return fmt.Errorf("-upstream is required")
	}
	// Refuse to become an open relay by omission. Either name who may use it, or say you meant to.
	if len(allow) == 0 && !*allowAny {
		return fmt.Errorf("pass -allow with the addresses that may use this relay, or -allow-any")
	}
	allowed, err := allow.prefixes()
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    *listen,
		Handler: newHandler(*upstream, allowed),
		// A slow client should not hold a goroutine forever.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout on purpose: a streamed answer runs for minutes and a deadline would cut it off.
	}

	log.Printf("relaying %s -> %s", *listen, *upstream)
	if *allowAny {
		log.Printf("WARNING: any source may use this relay")
	} else {
		log.Printf("allowed sources: %s", allow.String())
	}
	return srv.ListenAndServe()
}

// newHandler builds the proxy plus the source check in front of it.
func newHandler(upstream string, allowed []netip.Prefix) http.Handler {
	proxy := newProxy(upstream)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		src := sourceIP(r.RemoteAddr)
		if !allowedSource(src, allowed) {
			log.Printf("%s %s %s -> 403 (source not allowed)", src, r.Method, r.URL.Path)
			http.Error(w, "not allowed", http.StatusForbidden)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		proxy.ServeHTTP(rec, r)
		// A local record of what the relay carried, independent of the gateway's own logs.
		log.Printf("%s %s %s -> %d (%s)", src, r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// newProxy forwards to upstream over plain HTTP, which is what Aperture expects on a tailnet.
func newProxy(upstream string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// Rewrite rather than Director: Rewrite adds no X-Forwarded-For of its own, so the upstream gets
		// nothing hinting at who the real caller was. Any that arrived are dropped for the same reason.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: upstream})
			r.Out.Host = upstream
			for _, h := range []string{
				"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded",
			} {
				r.Out.Header.Del(h)
			}
		},
		// Answers stream, so pass every write straight through instead of buffering them.
		FlushInterval: -1,
		ErrorLog:      log.Default(),
	}
}

// allowedSource reports whether a connection may use the relay. Tailscale ACLs should be the real
// control; this is a second lock on the same door. An address that cannot be parsed is refused.
func allowedSource(src netip.Addr, allowed []netip.Prefix) bool {
	if len(allowed) == 0 {
		return true // -allow-any; the startup log says so
	}
	if !src.IsValid() {
		return false
	}
	for _, p := range allowed {
		if p.Contains(src) {
			return true
		}
	}
	return false
}

// sourceIP pulls the address out of a RemoteAddr, which carries a port and may be an IPv6 literal.
func sourceIP(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// statusRecorder remembers the status code so it can be logged. It intentionally does not touch the
// body, which keeps streaming responses flowing untouched.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets the proxy reach the underlying writer for flushing, which streaming depends on.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// repeatedFlag collects a flag given more than once.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }

func (r *repeatedFlag) Set(value string) error {
	// Commas as well as repeats, so a service file can hold the whole list in one variable.
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return fmt.Errorf("empty value")
		}
		*r = append(*r, entry)
	}
	return nil
}

// prefixes turns the values into networks, accepting a bare address as a single host.
func (r *repeatedFlag) prefixes() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(*r))
	for _, entry := range *r {
		if strings.Contains(entry, "/") {
			p, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("%q is not a CIDR: %w", entry, err)
			}
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR: %w", entry, err)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}
