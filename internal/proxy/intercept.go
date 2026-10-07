package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// handshakeTimeout bounds the TLS handshake with an intercepted client.
const handshakeTimeout = 30 * time.Second

// logf writes an audit line: to Server.Logf, or the standard logger.
func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// interceptable reports whether a CONNECT to target is terminated here rather
// than tunnelled blind: a CA is configured, the policy lists the target
// explicitly (hosts allowed only by AnyPublic are never intercepted), and the
// CA covers its host.
func (s *Server) interceptable(target string) bool {
	if s.CA == nil || !s.Policy.Explicit(target) {
		return false
	}
	host, _, err := net.SplitHostPort(target)
	return err == nil && s.CA.Covers(host)
}

// handleIntercept terminates TLS for an allowed CONNECT target with a
// certificate from the session CA and serves the HTTP/1.1 requests inside,
// forwarding each to the real target over a verified TLS connection.
func (s *Server) handleIntercept(w http.ResponseWriter, r *http.Request, target string) {
	host, _, _ := net.SplitHostPort(target)

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

	// A client that pipelines its ClientHello behind the CONNECT can have it
	// land in the hijacked reader's buffer: read through that reader.
	var conn net.Conn = client
	if rw != nil && rw.Reader.Buffered() > 0 {
		conn = bufferedConn{Conn: client, r: rw.Reader}
	}

	tlsConn := tls.Server(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// HTTP/1.1 only: a client that offers h2 falls back to it.
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			// The SNI must be the host the CONNECT was allowed for, or the
			// client could use one allowed host to reach another.
			if !strings.EqualFold(hello.ServerName, host) {
				return nil, &sniError{got: hello.ServerName, want: host}
			}
			return s.CA.LeafFor(host)
		},
	})
	ctx, cancel := context.WithTimeout(r.Context(), handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		// A client that doesn't trust the session CA (a pinned certificate, a
		// tool with its own trust store) ends up here.
		log.Printf("oc-proxy: TLS handshake for %s failed: %v", target, err)
		return
	}

	ln := newSingleConnListener(tlsConn)
	srv := &http.Server{
		Handler:           s.interceptHandler(target),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	// Serve returns once the connection has been closed.
	srv.Serve(ln)
}

type sniError struct{ got, want string }

func (e *sniError) Error() string {
	return "oc-proxy: SNI " + e.got + " doesn't match the CONNECT target " + e.want
}

// interceptHandler serves one intercepted connection's requests.
func (s *Server) interceptHandler(target string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		status, err := s.serveIntercepted(w, r, target)
		// Only host, method, path and status: never the query string (it can
		// carry secrets), headers or bodies. An upstream failure adds its error,
		// which names no URL, so a 502 can be diagnosed afterwards.
		line := fmt.Sprintf("oc-proxy: %s https://%s%s -> %d (%s)", r.Method, target, r.URL.EscapedPath(), status, time.Since(start).Round(time.Millisecond))
		if err != nil {
			line += ": " + err.Error()
		}
		s.logf("%s", line)
	})
}

// serveIntercepted handles one request and returns the status it answered
// with, and the upstream error behind a 502.
func (s *Server) serveIntercepted(w http.ResponseWriter, r *http.Request, target string) (int, error) {
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "oc-proxy: Upgrade (WebSocket) isn't supported on an intercepted connection", http.StatusNotImplemented)
		return http.StatusNotImplemented, nil
	}
	if !hostMatches(r.Host, target) {
		http.Error(w, "oc-proxy: Host header "+r.Host+" doesn't match the CONNECT target "+target, http.StatusMisdirectedRequest)
		return http.StatusMisdirectedRequest, nil
	}
	if s.OnRequest != nil {
		if err := s.OnRequest(r, target); err != nil {
			http.Error(w, "oc-proxy: request refused: "+err.Error(), http.StatusForbidden)
			return http.StatusForbidden, nil
		}
	}

	// Always go to the CONNECT target, whatever the request says: the policy
	// check was made on it.
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = "https"
	out.URL.Host = target
	removeHopByHop(out.Header)

	resp, err := s.upstream(target).RoundTrip(out)
	if err != nil {
		http.Error(w, "oc-proxy: upstream "+target+": "+err.Error(), http.StatusBadGateway)
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	relay(w, resp)
	return resp.StatusCode, nil
}

// hostMatches reports whether a request's Host header names target
// ("host:port"): the same host, and the same port or none.
func hostMatches(requestHost, target string) bool {
	wantHost, wantPort, _ := net.SplitHostPort(target)
	host, port, err := net.SplitHostPort(requestHost)
	if err != nil {
		host, port = requestHost, ""
	}
	return strings.EqualFold(host, wantHost) && (port == "" || port == wantPort)
}

// upstream is the transport for target, reused across requests so connections
// to it are kept alive. It dials the way the policy says (dialerFor) and
// verifies the real certificate against UpstreamTLS, or the system roots.
func (s *Server) upstream(target string) *http.Transport {
	if t, ok := s.upstreams.Load(target); ok {
		return t.(*http.Transport)
	}
	t := forwardTransport.Clone()
	t.DialContext = s.dialerFor(target).DialContext
	if s.UpstreamTLS != nil {
		t.TLSClientConfig = s.UpstreamTLS.Clone()
	}
	actual, _ := s.upstreams.LoadOrStore(target, t)
	return actual.(*http.Transport)
}

// bufferedConn reads through r, which may hold bytes already taken off the
// connection.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// singleConnListener hands out one connection and then reports itself closed
// once that connection has been, so http.Server.Serve returns when it ends.
type singleConnListener struct {
	conn  net.Conn
	given bool
	mu    sync.Mutex
	done  chan struct{}
}

func newSingleConnListener(c net.Conn) *singleConnListener {
	l := &singleConnListener{done: make(chan struct{})}
	l.conn = &notifyConn{Conn: c, onClose: sync.OnceFunc(func() { close(l.done) })}
	return l
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.given {
		l.given = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error   { return l.conn.Close() }
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// notifyConn runs onClose when it's closed.
type notifyConn struct {
	net.Conn
	onClose func()
}

func (c *notifyConn) Close() error {
	defer c.onClose()
	return c.Conn.Close()
}
