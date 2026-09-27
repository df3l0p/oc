// Package proxy implements the sandbox's egress forward proxy: a host
// allow/blocklist first, with TLS interception and credential injection
// layered on top in later phases.
package proxy

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// forwardTransport is a copy of http.DefaultTransport with proxying
// disabled. handleForward's sandbox already reaches this server via its own
// HTTP_PROXY/HTTPS_PROXY, but the Docker CLI can also inject an HTTP_PROXY
// into this proxy container's own environment from the operator's
// ~/.docker/config.json — this server must always dial the target directly,
// never chase that.
var forwardTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}()

// Policy decides which upstream destinations may be reached. Entries are
// exact "host:port" strings, matching what a CONNECT target or a
// normalized absolute-URI request host already looks like.
type Policy struct {
	Allow map[string]bool
}

// NewPolicy builds a Policy allowing exactly the given "host:port" entries.
func NewPolicy(hostports []string) Policy {
	m := make(map[string]bool, len(hostports))
	for _, h := range hostports {
		m[h] = true
	}
	return Policy{Allow: m}
}

// Allows reports whether hostport may be reached.
func (p Policy) Allows(hostport string) bool {
	return p.Allow[hostport]
}

// Server is the sandbox's egress proxy. It implements http.Handler and is
// meant to be run behind http.ListenAndServe.
type Server struct {
	Policy Policy
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	s.handleForward(w, r)
}

// handleConnect blind-tunnels an allowed CONNECT target: bytes are relayed
// unmodified in both directions, never inspected.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if !s.Policy.Allows(target) {
		log.Printf("oc-proxy: denied %s", target)
		http.Error(w, "oc-proxy: host not allowed: "+target, http.StatusForbidden)
		return
	}
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		http.Error(w, "oc-proxy: dial upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "oc-proxy: hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "oc-proxy: hijack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	// A client that pipelines its first bytes (e.g. a TLS ClientHello) onto
	// the same write as the CONNECT request can have them land in the
	// Hijacked bufio.Reader's buffer rather than still on the wire: flush
	// those upstream before wiring up the copies, or they're silently lost.
	if rw != nil && rw.Reader.Buffered() > 0 {
		buffered := make([]byte, rw.Reader.Buffered())
		if _, err := io.ReadFull(rw.Reader, buffered); err != nil {
			return
		}
		if _, err := upstream.Write(buffered); err != nil {
			return
		}
	}

	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

// hostport normalizes u's authority to an explicit "host:port", defaulting
// to port 80 the way a plain (non-TLS) HTTP request would.
func hostport(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), "80")
}

// hopByHop are headers that describe a single connection, not the message,
// and must not be forwarded by a proxy (RFC 9110 §7.6.1).
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHop(h http.Header) {
	for _, f := range strings.Split(h.Get("Connection"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			h.Del(f)
		}
	}
	for _, k := range hopByHop {
		h.Del(k)
	}
}

// handleForward proxies a plain (non-CONNECT) absolute-URI request to an
// allowed host. The response is flushed to the client chunk by chunk, so
// streamed responses (llama-server's SSE completions) aren't held back.
func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	target := hostport(r.URL)
	if !s.Policy.Allows(target) {
		log.Printf("oc-proxy: denied %s", target)
		http.Error(w, "oc-proxy: host not allowed: "+target, http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = "" // must be empty on a client request
	removeHopByHop(out.Header)

	resp, err := forwardTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, "oc-proxy: forward: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil {
			return
		}
	}
}
