package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

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
