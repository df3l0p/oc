package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain silences the package's denial logging so a passing `go test -v`
// stays readable: TestHandleForwardBlocksUnconfiguredHost and
// TestHandleConnectBlocksUnconfiguredHost both deliberately trigger it.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

func TestHandleForwardAllowsConfiguredHost(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "backend ok")
	}))
	defer backend.Close()
	backendHost := mustParseURL(t, backend.URL).Host

	srv := &Server{Policy: NewPolicy([]string{backendHost})}
	px := httptest.NewServer(srv)
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "backend ok" {
		t.Errorf("body = %q, want %q", body, "backend ok")
	}
}

func TestHandleForwardBlocksUnconfiguredHost(t *testing.T) {
	reached := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer backend.Close()

	srv := &Server{Policy: NewPolicy(nil)} // empty allow-list
	px := httptest.NewServer(srv)
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if reached {
		t.Error("backend must not be reached for a blocked host")
	}
}

// opencode streams completions from llama-server (SSE); the forwarder must
// hand each chunk to the client as it arrives, not when the upstream ends.
func TestHandleForwardStreamsChunksWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: second\n\n")
	}))
	defer backend.Close()

	srv := &Server{Policy: NewPolicy([]string{mustParseURL(t, backend.URL).Host})}
	px := httptest.NewServer(srv)
	defer px.Close()
	// Deferred after the servers, so it runs before their Close: each Close
	// waits for in-flight handlers, which wait on release.
	defer unblock()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "data: first\n" {
			t.Errorf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk never arrived while the upstream was still open: the forwarder is buffering")
	}
}

// TestHandleForwardTransportIgnoresProxyEnvVars guards against the Docker
// CLI injecting an HTTP_PROXY into the proxy container's environment from
// the operator's ~/.docker/config.json: the transport handleForward uses to
// reach the target must never chase env-configured proxying.
//
// A behavioral variant (t.Setenv("HTTP_PROXY", ...) against a bogus listener,
// asserting it's never dialed) was tried first, but Go's env-based proxy
// resolution is memoized once per process the first time anything calls
// through http.DefaultTransport's default Proxy func — which an earlier test
// in this same binary already does — so t.Setenv has no effect on it by the
// time this test runs, regardless of whether the fix is present. That makes
// it an unreliable regression test, so this asserts the structural fact that
// actually guarantees the behavior instead: the transport's Proxy field is a
// static nil, never consulting the environment at all.
func TestHandleForwardTransportIgnoresProxyEnvVars(t *testing.T) {
	if forwardTransport.Proxy != nil {
		t.Error("forwardTransport.Proxy must be nil: the proxy must never honor HTTP_PROXY/HTTPS_PROXY env vars for its own outbound requests")
	}
}

func TestHandleForwardDropsHopByHopHeaders(t *testing.T) {
	var gotProxyConn string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProxyConn = r.Header.Get("Proxy-Connection")
	}))
	defer backend.Close()

	srv := &Server{Policy: NewPolicy([]string{mustParseURL(t, backend.URL).Host})}
	px := httptest.NewServer(srv)
	defer px.Close()

	req, _ := http.NewRequest(http.MethodGet, backend.URL, nil)
	req.Header.Set("Proxy-Connection", "keep-alive")
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if gotProxyConn != "" {
		t.Errorf("upstream saw Proxy-Connection = %q; hop-by-hop headers must not be forwarded", gotProxyConn)
	}
}

// readConnectStatusLine reads and discards a CONNECT response's status line
// and headers, leaving br positioned at the start of the tunneled bytes.
func readConnectStatusLine(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading CONNECT status line: %v", err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading CONNECT headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	return status
}

func TestHandleConnectTunnelsAllowedHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn) // echo
	}()

	srv := &Server{Policy: NewPolicy([]string{ln.Addr().String()})}
	px := httptest.NewServer(srv)
	defer px.Close()

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", ln.Addr().String(), ln.Addr().String())

	br := bufio.NewReader(conn)
	status := readConnectStatusLine(t, br)
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status line = %q, want 200", status)
	}

	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo = %q, want %q", buf, "ping")
	}
}

// TestHandleConnectFlushesHijackBufferedBytes covers a client that pipelines
// its first bytes (e.g. a TLS ClientHello) onto the same write as the
// CONNECT request: http.Server's bufio.Reader can read both in one syscall,
// leaving the ClientHello sitting in the Hijacked reader's buffer rather
// than on the wire. If handleConnect only wires up io.Copy(upstream, client)
// after that, those buffered bytes are silently dropped and the tunnel
// deadlocks waiting for a reply that never comes.
func TestHandleConnectFlushesHijackBufferedBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err == nil {
			received <- buf
		}
	}()

	srv := &Server{Policy: NewPolicy([]string{ln.Addr().String()})}
	px := httptest.NewServer(srv)
	defer px.Close()

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The CONNECT request and the "payload" go out in a single Write, so a
	// single Read on the server side (and hence a single bufio fill) is very
	// likely to capture both.
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nping",
		ln.Addr().String(), ln.Addr().String())

	br := bufio.NewReader(conn)
	status := readConnectStatusLine(t, br)
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status line = %q, want 200", status)
	}

	select {
	case buf := <-received:
		if string(buf) != "ping" {
			t.Errorf("upstream received %q, want %q", buf, "ping")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received the bytes buffered alongside the CONNECT request")
	}
}

func TestHandleConnectBlocksUnconfiguredHost(t *testing.T) {
	srv := &Server{Policy: NewPolicy(nil)}
	px := httptest.NewServer(srv)
	defer px.Close()

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(conn, "CONNECT example.invalid:443 HTTP/1.1\r\nHost: example.invalid:443\r\n\r\n")

	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "403") {
		t.Errorf("CONNECT status line = %q, want 403", status)
	}
}

// Only plain http:// may use the forward path: an https:// URL sent there
// would be policy-checked against port 80 (hostport's default) while the
// transport dials 443. https must go through CONNECT instead.
func TestHandleForwardRefusesNonHTTPSchemes(t *testing.T) {
	srv := &Server{Policy: NewPolicy([]string{"example.invalid:80", "example.invalid:443"})}
	px := httptest.NewServer(srv)
	defer px.Close()

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(conn, "GET https://example.invalid/ HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "400") {
		t.Errorf("status line = %q, want 400", status)
	}
}

// A client that half-closes (shutdown(SHUT_WR)) after sending must still get
// the whole response: the tunnel may only close a side once both directions
// are done.
func TestHandleConnectKeepsTheResponseAfterAClientHalfClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn) // until the client's half-close arrives
		conn.Write([]byte("pong"))
	}()

	srv := &Server{Policy: NewPolicy([]string{ln.Addr().String()})}
	px := httptest.NewServer(srv)
	defer px.Close()

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", ln.Addr().String(), ln.Addr().String())
	br := bufio.NewReader(conn)
	if status := readConnectStatusLine(t, br); !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status line = %q, want 200", status)
	}

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("reading the response after half-close: %v", err)
	}
	if string(got) != "pong" {
		t.Errorf("response after half-close = %q, want %q", got, "pong")
	}
}

// Host names are case-insensitive, so the allow-list must be too.
func TestPolicyMatchesHostsCaseInsensitively(t *testing.T) {
	pol := NewPolicy([]string{"GitHub.com:443"})
	if !pol.Allows("github.COM:443") {
		t.Error("an allowed host must match regardless of case")
	}
	if pol.Allows("github.com:80") {
		t.Error("case folding must not loosen the port match")
	}
}

// connectThroughProxy opens a CONNECT tunnel to target through a proxy
// allowing it, with a short half-close idle timeout, returning the client
// end and its reader.
func connectThroughProxy(t *testing.T, target string) (net.Conn, *bufio.Reader) {
	t.Helper()
	px := httptest.NewServer(&Server{Policy: NewPolicy([]string{target}), HalfCloseIdle: 100 * time.Millisecond})
	t.Cleanup(px.Close)
	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	if status := readConnectStatusLine(t, br); !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status line = %q, want 200", status)
	}
	return conn, br
}

// Once the client has finished, an upstream that goes quiet without ever
// closing must not hold the tunnel open forever.
func TestHandleConnectClosesAnIdleTunnelAfterTheClientFinishes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn) // sees the client's half-close...
		<-release                 // ...then never answers or closes
	}()

	conn, br := connectThroughProxy(t, ln.Addr().String())
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("tunnel wasn't closed after going idle: %v", err)
	}
}

// The idle timeout only fires when nothing moves: a response still
// streaming after the client's half-close runs to completion, however long.
func TestHandleConnectKeepsAnActiveTunnelPastTheIdleTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn)
		for _, chunk := range []string{"a", "b", "c", "d"} {
			time.Sleep(60 * time.Millisecond) // 240ms in total, each gap under the timeout
			conn.Write([]byte(chunk))
		}
	}()

	conn, br := connectThroughProxy(t, ln.Addr().String())
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcd" {
		t.Errorf("response after half-close = %q, want %q", got, "abcd")
	}
}
