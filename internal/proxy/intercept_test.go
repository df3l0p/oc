package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// auditLog collects the lines the proxy logs about intercepted requests.
type auditLog struct {
	mu    sync.Mutex
	lines []string
}

func (a *auditLog) Logf(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, fmt.Sprintf(format, args...))
}

func (a *auditLog) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.lines, "\n")
}

// waitForLog polls until the audit log contains want: a line is written once a
// response has been relayed, which can be just after the client has read it.
func waitForLog(t *testing.T, a *auditLog, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := a.String(); strings.Contains(s, want) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("audit log never contained %q; got:\n%s", want, a.String())
	return ""
}

// newUpstream starts an https server named "localhost" whose certificate comes
// from its own CA, never the proxy's. It returns the "localhost:port" the
// proxy is asked to CONNECT to, and a pool trusting that CA.
func newUpstream(t *testing.T, h http.Handler) (hostport string, trust *x509.CertPool) {
	t.Helper()
	ca, err := NewCA([]string{"localhost"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	leaf, err := ca.LeafFor("localhost")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	trust = x509.NewCertPool()
	trust.AppendCertsFromPEM(ca.CertPEM())
	return "localhost:" + mustParseURL(t, srv.URL).Port(), trust
}

// newProxyFor starts the proxy with policy allowing hostport and a CA that
// covers localhost, plus an audit log, trusting upstreamTrust for the real
// upstream. It returns the proxy's URL, its CA and the audit log.
func newProxyFor(t *testing.T, hostport string, upstreamTrust *x509.CertPool) (*url.URL, *CA, *auditLog) {
	t.Helper()
	ca, err := NewCA([]string{"localhost"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	audit := &auditLog{}
	srv := &Server{
		Policy:      NewPolicy([]string{hostport}),
		CA:          ca,
		Logf:        audit.Logf,
		UpstreamTLS: &tls.Config{RootCAs: upstreamTrust},
	}
	px := httptest.NewServer(srv)
	t.Cleanup(px.Close)
	return mustParseURL(t, px.URL), ca, audit
}

// clientVia is an https client that goes through the proxy and trusts only
// the given pool.
func clientVia(proxyURL *url.URL, trust *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: trust},
		},
		Timeout: 10 * time.Second,
	}
}

func poolOf(ca *CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(ca.CertPEM())
	return p
}

func TestInterceptServesTheRequestWithTheProxyCA(t *testing.T) {
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from "+r.URL.Path)
	}))
	proxyURL, ca, _ := newProxyFor(t, hostport, upstreamTrust)

	// The client trusts only the proxy's CA. The upstream's certificate comes
	// from another CA, so this can only succeed if the proxy terminated TLS.
	resp, err := clientVia(proxyURL, poolOf(ca)).Get("https://" + hostport + "/some/path")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello from /some/path" {
		t.Errorf("got %d %q, want 200 \"hello from /some/path\"", resp.StatusCode, body)
	}
}

func TestInterceptForwardsRequestBodyAndMethod(t *testing.T) {
	var gotMethod, gotBody string
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotBody = r.Method, string(b)
	}))
	proxyURL, ca, _ := newProxyFor(t, hostport, upstreamTrust)

	resp, err := clientVia(proxyURL, poolOf(ca)).Post("https://"+hostport+"/", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()
	if gotMethod != http.MethodPost || gotBody != "payload" {
		t.Errorf("upstream saw %s %q, want POST \"payload\"", gotMethod, gotBody)
	}
}

func TestInterceptAuditLogHasMethodHostPathAndStatusButNoQuery(t *testing.T) {
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	proxyURL, ca, audit := newProxyFor(t, hostport, upstreamTrust)

	req, _ := http.NewRequest("GET", "https://"+hostport+"/repos/x?token=s3cret", nil)
	req.Header.Set("Authorization", "Bearer s3cret-header")
	resp, err := clientVia(proxyURL, poolOf(ca)).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	line := waitForLog(t, audit, "/repos/x")
	for _, want := range []string{"GET", "localhost", "418"} {
		if !strings.Contains(line, want) {
			t.Errorf("audit log %q is missing %q", line, want)
		}
	}
	for _, secret := range []string{"token", "s3cret", "Authorization", "Bearer"} {
		if strings.Contains(line, secret) {
			t.Errorf("audit log %q must not contain %q: only host, method, path and status are logged", line, secret)
		}
	}
}

func TestInterceptRefusesAHostHeaderThatDisagreesWithTheConnectTarget(t *testing.T) {
	var reached atomic.Bool
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	proxyURL, ca, _ := newProxyFor(t, hostport, upstreamTrust)

	req, _ := http.NewRequest("GET", "https://"+hostport+"/", nil)
	req.Host = "evil.example"
	resp, err := clientVia(proxyURL, poolOf(ca)).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMisdirectedRequest)
	}
	if reached.Load() {
		t.Error("a request whose Host differs from the CONNECT target must not reach the upstream")
	}
}

func TestInterceptRefusesAnSNIThatDisagreesWithTheConnectTarget(t *testing.T) {
	var reached atomic.Bool
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	proxyURL, ca, _ := newProxyFor(t, hostport, upstreamTrust)

	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: poolOf(ca), ServerName: "other.test"},
	}, Timeout: 10 * time.Second}
	resp, err := client.Get("https://" + hostport + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("handshake with a mismatched SNI succeeded, want a failure")
	}
	if reached.Load() {
		t.Error("the upstream must not be reached after a failed handshake")
	}
}

func TestInterceptLeavesHostsOutsideTheCAAsBlindTunnels(t *testing.T) {
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "real upstream")
	}))
	// The CA covers a different host, so localhost is allowed but not
	// intercepted (what an -all-net host looks like to the CA).
	otherCA, err := NewCA([]string{"other.test"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	audit := &auditLog{}
	srv := &Server{Policy: NewPolicy([]string{hostport}), CA: otherCA, Logf: audit.Logf}
	px := httptest.NewServer(srv)
	defer px.Close()

	// Trusting only the upstream's own CA works only if the proxy relayed the
	// upstream's real certificate.
	resp, err := clientVia(mustParseURL(t, px.URL), upstreamTrust).Get("https://" + hostport + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "real upstream" {
		t.Errorf("body = %q, want %q", body, "real upstream")
	}
	if got := audit.String(); got != "" {
		t.Errorf("a blind tunnel must not produce audit lines, got %q", got)
	}
}

func TestInterceptStreamsResponsesWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: two\n\n")
	}))
	defer close(release) // registered after the upstream's cleanup, so it runs first
	proxyURL, ca, _ := newProxyFor(t, hostport, upstreamTrust)

	resp, err := clientVia(proxyURL, poolOf(ca)).Get("https://" + hostport + "/events")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if line != "data: one\n" {
			t.Errorf("first line = %q, want %q", line, "data: one\n")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event was held back until the response finished: streaming is broken")
	}
}

func TestInterceptAnswers502WhenTheUpstreamCertificateIsNotTrusted(t *testing.T) {
	hostport, _ := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "must not be served")
	}))
	// The proxy is given an empty trust pool, so the real upstream's
	// certificate fails verification there.
	proxyURL, ca, audit := newProxyFor(t, hostport, x509.NewCertPool())

	resp, err := clientVia(proxyURL, poolOf(ca)).Get("https://" + hostport + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	// The audit line must say why, or a 502 can't be diagnosed after the fact.
	if line := waitForLog(t, audit, "502"); !strings.Contains(line, "certificate") {
		t.Errorf("audit log %q should include the upstream error", line)
	}
}

func TestInterceptAnswers501ForUpgradeRequests(t *testing.T) {
	var reached atomic.Bool
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	proxyURL, ca, _ := newProxyFor(t, hostport, upstreamTrust)

	req, _ := http.NewRequest("GET", "https://"+hostport+"/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := clientVia(proxyURL, poolOf(ca)).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
	if reached.Load() {
		t.Error("an Upgrade request must not reach the upstream")
	}
}

func TestInterceptOnRequestHookCanChangeTheRequest(t *testing.T) {
	var got string
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-From-Hook")
	}))
	ca, _ := NewCA([]string{"localhost"})
	var hookTarget string
	srv := &Server{
		Policy:      NewPolicy([]string{hostport}),
		CA:          ca,
		Logf:        (&auditLog{}).Logf,
		UpstreamTLS: &tls.Config{RootCAs: upstreamTrust},
		OnRequest: func(r *http.Request, target string) error {
			hookTarget = target
			r.Header.Set("X-From-Hook", "set")
			return nil
		},
	}
	px := httptest.NewServer(srv)
	defer px.Close()

	resp, err := clientVia(mustParseURL(t, px.URL), poolOf(ca)).Get("https://" + hostport + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if got != "set" {
		t.Errorf("upstream saw X-From-Hook = %q, want %q", got, "set")
	}
	if hookTarget != hostport {
		t.Errorf("hook target = %q, want %q", hookTarget, hostport)
	}
}

func TestInterceptOnRequestHookErrorAnswers403(t *testing.T) {
	var reached atomic.Bool
	hostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	ca, _ := NewCA([]string{"localhost"})
	srv := &Server{
		Policy:      NewPolicy([]string{hostport}),
		CA:          ca,
		Logf:        (&auditLog{}).Logf,
		UpstreamTLS: &tls.Config{RootCAs: upstreamTrust},
		OnRequest:   func(*http.Request, string) error { return errors.New("not today") },
	}
	px := httptest.NewServer(srv)
	defer px.Close()

	resp, err := clientVia(mustParseURL(t, px.URL), poolOf(ca)).Get("https://" + hostport + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if reached.Load() {
		t.Error("a request refused by the hook must not reach the upstream")
	}
}

func TestInterceptedHostIsStillSubjectToThePolicy(t *testing.T) {
	// A CA covering the host doesn't make it reachable: the policy decides.
	ca, _ := NewCA([]string{"localhost"})
	srv := &Server{Policy: NewPolicy(nil), CA: ca, Logf: (&auditLog{}).Logf}
	px := httptest.NewServer(srv)
	defer px.Close()

	client := clientVia(mustParseURL(t, px.URL), poolOf(ca))
	resp, err := client.Get("https://localhost:9/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("request to a host outside the policy succeeded")
	}
}
