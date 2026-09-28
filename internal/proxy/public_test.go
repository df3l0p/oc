package proxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":         true,
		"140.82.112.3":    true, // github.com
		"2606:4700::1111": true,
		"10.1.2.3":        false, // RFC 1918
		"172.17.0.1":      false, // docker bridge gateway
		"192.168.65.254":  false, // Docker Desktop's host
		"127.0.0.1":       false,
		"169.254.169.254": false, // link-local (cloud metadata)
		"100.101.102.103": false, // CGNAT / Tailscale
		"0.0.0.0":         false,
		"224.0.0.1":       false, // multicast
		"::1":             false,
		"fd7a:115c::1":    false, // unique local
		"fe80::1":         false,
		"::ffff:10.0.0.1": false, // IPv4-mapped private
	} {
		if got := isPublic(net.ParseIP(addr)); got != want {
			t.Errorf("isPublic(%s) = %v, want %v", addr, got, want)
		}
	}
}

// With *, a destination that isn't listed may only be a public address: the
// host machine, the LAN and loopback stay out of reach even by IP.
func TestAnyPublicStillRefusesUnlistedNonPublicAddresses(t *testing.T) {
	reached := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer backend.Close()
	backendHost := mustParseURL(t, backend.URL).Host // 127.0.0.1:<port>

	px := httptest.NewServer(&Server{Policy: Policy{AnyPublic: true}})
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("forward to unlisted loopback: status = %d, want 403", resp.StatusCode)
	}

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", backendHost, backendHost)
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "403") {
		t.Errorf("CONNECT to unlisted loopback: status line = %q, want 403", status)
	}
	if reached {
		t.Error("an unlisted non-public address must never be reached")
	}
}

// Listed entries (llama-server, a private address) keep working alongside *.
func TestAnyPublicKeepsListedNonPublicEntries(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer backend.Close()

	pol := NewPolicy([]string{mustParseURL(t, backend.URL).Host})
	pol.AnyPublic = true
	px := httptest.NewServer(&Server{Policy: pol})
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("listed entry under *: status = %d, want 200", resp.StatusCode)
	}
}
