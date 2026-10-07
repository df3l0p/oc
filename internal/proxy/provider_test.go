package proxy

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testPlaceholder = "oc_placeholder_0123456789abcdef"
	testSecret      = "github_pat_REALSECRET"
)

func newTestInjector(t *testing.T, hosts ...string) (*Injector, *auditLog) {
	t.Helper()
	p, err := NewStaticProvider("github", hosts, testSecret, testPlaceholder)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	audit := &auditLog{}
	return &Injector{Providers: []Provider{p}, Logf: audit.Logf}, audit
}

func newReq(url, auth string) *http.Request {
	r := httptest.NewRequest("GET", url, nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestInjectorReplacesThePlaceholderInBearerAndTokenSchemes(t *testing.T) {
	in, _ := newTestInjector(t, "api.github.com:443")
	for _, scheme := range []string{"Bearer", "token"} {
		r := newReq("https://api.github.com/user", scheme+" "+testPlaceholder)
		if err := in.OnRequest(r, "api.github.com:443"); err != nil {
			t.Fatalf("%s: OnRequest: %v", scheme, err)
		}
		if got, want := r.Header.Get("Authorization"), scheme+" "+testSecret; got != want {
			t.Errorf("%s: Authorization = %q, want %q", scheme, got, want)
		}
	}
}

// git sends the credential as the Basic-auth password.
func TestInjectorRewritesBasicAuth(t *testing.T) {
	in, _ := newTestInjector(t, "github.com:443")
	enc := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + testPlaceholder))
	r := newReq("https://github.com/o/r.git/info/refs?service=git-upload-pack", "Basic "+enc)
	if err := in.OnRequest(r, "github.com:443"); err != nil {
		t.Fatalf("OnRequest: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Basic "))
	if err != nil {
		t.Fatalf("Authorization isn't valid Basic: %v", err)
	}
	if string(raw) != "x-access-token:"+testSecret {
		t.Errorf("decoded credentials = %q", raw)
	}
}

func TestInjectorLeavesRequestsWithoutThePlaceholderAlone(t *testing.T) {
	in, audit := newTestInjector(t, "api.github.com:443")
	for _, auth := range []string{"", "Bearer something-else"} {
		r := newReq("https://api.github.com/user", auth)
		if err := in.OnRequest(r, "api.github.com:443"); err != nil {
			t.Fatalf("auth %q: OnRequest: %v", auth, err)
		}
		if got := r.Header.Get("Authorization"); got != auth {
			t.Errorf("Authorization changed from %q to %q", auth, got)
		}
	}
	if s := audit.String(); s != "" {
		t.Errorf("nothing was injected, but the injector logged: %s", s)
	}
}

func TestInjectorRefusesThePlaceholderOutsideTheAuthorizationHeader(t *testing.T) {
	in, _ := newTestInjector(t, "api.github.com:443")
	cases := map[string]*http.Request{
		"query":  newReq("https://api.github.com/user?access_token="+testPlaceholder, ""),
		"path":   newReq("https://api.github.com/"+testPlaceholder, ""),
		"header": newReq("https://api.github.com/user", ""),
	}
	cases["header"].Header.Set("X-Other", testPlaceholder)
	for name, r := range cases {
		if err := in.OnRequest(r, "api.github.com:443"); err == nil {
			t.Errorf("%s: want the request refused", name)
		}
	}
}

func TestInjectorRefusesThePlaceholderAtAnUnboundHost(t *testing.T) {
	in, _ := newTestInjector(t, "api.github.com:443")
	for _, auth := range []string{
		"Bearer " + testPlaceholder,
		"Basic " + base64.StdEncoding.EncodeToString([]byte("u:"+testPlaceholder)),
	} {
		r := newReq("https://evil.example/steal", auth)
		if err := in.OnRequest(r, "evil.example:443"); err == nil {
			t.Errorf("auth %q: want the request refused", auth)
		}
		if strings.Contains(r.Header.Get("Authorization"), testSecret) {
			t.Error("the secret was injected at an unbound host")
		}
	}
}

func TestInjectorMatchesHostsCaseInsensitively(t *testing.T) {
	in, _ := newTestInjector(t, "api.github.com:443")
	r := newReq("https://api.github.com/user", "Bearer "+testPlaceholder)
	if err := in.OnRequest(r, "API.GitHub.com:443"); err != nil {
		t.Fatalf("OnRequest: %v", err)
	}
	if r.Header.Get("Authorization") != "Bearer "+testSecret {
		t.Error("placeholder wasn't replaced for a differently-cased target")
	}
}

func TestInjectorLogsNeverContainTheSecretOrThePlaceholder(t *testing.T) {
	in, audit := newTestInjector(t, "api.github.com:443")
	in.OnRequest(newReq("https://api.github.com/user", "Bearer "+testPlaceholder), "api.github.com:443")
	in.OnRequest(newReq("https://evil.example/x?k="+testPlaceholder, ""), "evil.example:443")
	in.OnRequest(newReq("https://api.github.com/user?k="+testPlaceholder, ""), "api.github.com:443")
	log := audit.String()
	if log == "" {
		t.Fatal("expected audit lines for the injection and the refusals")
	}
	for _, bad := range []string{testSecret, testPlaceholder} {
		if strings.Contains(log, bad) {
			t.Errorf("log contains %q:\n%s", bad, log)
		}
	}
	if !strings.Contains(log, "injected") {
		t.Errorf("log doesn't record the injection:\n%s", log)
	}
}

type failingProvider struct{ *StaticProvider }

func (failingProvider) Credential(context.Context) (string, error) {
	return "", errors.New("token endpoint down: secret-ish detail")
}

func TestInjectorReturnsACredentialErrorWhenTheProviderFails(t *testing.T) {
	base, _ := NewStaticProvider("github", []string{"api.github.com:443"}, testSecret, testPlaceholder)
	in := &Injector{Providers: []Provider{failingProvider{base}}}
	err := in.OnRequest(newReq("https://api.github.com/user", "Bearer "+testPlaceholder), "api.github.com:443")
	var ce *CredentialError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a *CredentialError", err)
	}
	if strings.Contains(ce.Error(), "secret-ish") {
		t.Errorf("CredentialError.Error() leaks the underlying error: %q", ce.Error())
	}
}

func TestNewStaticProviderRejectsUnusableTokens(t *testing.T) {
	for _, tok := range []string{"", "abc\n", "abc def", "abc\x00", " lead", "tab\there"} {
		if _, err := NewGitHubProvider(tok, testPlaceholder); err == nil {
			t.Errorf("token %q: want an error", tok)
		}
		if ValidateToken(tok) == nil {
			t.Errorf("ValidateToken(%q): want an error", tok)
		}
	}
	if _, err := NewGitHubProvider("ghp_okToken123", ""); err == nil {
		t.Error("an empty placeholder must be rejected")
	}
	if _, err := NewGitHubProvider("github_pat_ok", testPlaceholder); err != nil {
		t.Errorf("a normal token was rejected: %v", err)
	}
}

func TestNewGitHubProviderIsBoundToGitHubAndItsAPI(t *testing.T) {
	p, err := NewGitHubProvider("github_pat_ok", testPlaceholder)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Hosts(), ","); got != "github.com:443,api.github.com:443" {
		t.Errorf("Hosts = %s", got)
	}
	if p.Name() != "github" || p.Placeholder() != testPlaceholder {
		t.Errorf("Name/Placeholder = %q/%q", p.Name(), p.Placeholder())
	}
	if tok, err := p.Credential(context.Background()); err != nil || tok != "github_pat_ok" {
		t.Errorf("Credential = %q, %v", tok, err)
	}
}

// End to end through the proxy: what the real upstream receives. mk builds the
// provider once the upstream's address is known, so it can be bound to it.
func newInjectingProxy(t *testing.T, upstreamHandler http.Handler, mk func(hostport string) Provider) (c *injectClient, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	hostport, trust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		upstreamHandler.ServeHTTP(w, r)
	}))
	ca, err := NewCA([]string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Policy:      NewPolicy([]string{hostport}),
		CA:          ca,
		Logf:        (&auditLog{}).Logf,
		UpstreamTLS: &tls.Config{RootCAs: trust},
		OnRequest:   (&Injector{Providers: []Provider{mk(hostport)}}).OnRequest,
	}
	px := httptest.NewServer(srv)
	t.Cleanup(px.Close)
	return &injectClient{client: clientVia(mustParseURL(t, px.URL), poolOf(ca)), target: hostport}, hits
}

type injectClient struct {
	client *http.Client
	target string
}

// do sends GET /x<query> with the given Authorization and returns the status
// and body.
func (c *injectClient) do(t *testing.T, auth, query string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://"+c.target+"/x"+query, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func staticAt(t *testing.T) func(string) Provider {
	return func(hostport string) Provider {
		p, err := NewStaticProvider("github", []string{hostport}, testSecret, testPlaceholder)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
}

func TestInterceptInjectsTheCredentialUpstream(t *testing.T) {
	c, _ := newInjectingProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}), staticAt(t))
	_, body := c.do(t, "Bearer "+testPlaceholder, "")
	if body != "Bearer "+testSecret {
		t.Errorf("upstream saw Authorization %q, want the real credential", body)
	}
}

func TestInterceptAnswers502WhenTheCredentialIsUnavailable(t *testing.T) {
	c, hits := newInjectingProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		func(hostport string) Provider {
			base, _ := NewStaticProvider("github", []string{hostport}, testSecret, testPlaceholder)
			return failingProvider{base}
		})
	status, body := c.do(t, "Bearer "+testPlaceholder, "")
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", status)
	}
	if hits.Load() != 0 || strings.Contains(body, testPlaceholder) {
		t.Errorf("the request reached upstream (%d hits) or the body leaks the placeholder: %q", hits.Load(), body)
	}
}

func TestInterceptNeverForwardsAPlaceholderInTheQuery(t *testing.T) {
	c, hits := newInjectingProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), staticAt(t))
	status, _ := c.do(t, "", "?k="+testPlaceholder)
	if status != http.StatusForbidden || hits.Load() != 0 {
		t.Errorf("status = %d, upstream hits = %d; want 403 and 0", status, hits.Load())
	}
}
