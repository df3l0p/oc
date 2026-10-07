package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// Provider supplies a credential the proxy injects into requests to the hosts
// it is bound to. The sandbox only ever holds Placeholder; the proxy swaps in
// Credential on the way out.
type Provider interface {
	// Name identifies the provider in logs ("github").
	Name() string
	// Hosts are the "host:port" CONNECT targets the credential may be sent to.
	Hosts() []string
	// Placeholder is the opaque value the sandbox is given in place of the
	// credential.
	Placeholder() string
	// Credential is the current secret. A refreshing provider renews it here.
	Credential(ctx context.Context) (string, error)
}

// StaticProvider is a Provider whose credential never changes.
type StaticProvider struct {
	name        string
	hosts       []string
	token       string
	placeholder string
}

// ValidateToken rejects tokens that can't be sent safely in a header: empty,
// or containing whitespace or control characters (a trailing newline from a
// copy-paste is the usual culprit).
func ValidateToken(token string) error {
	if token == "" {
		return errors.New("the token is empty")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("the token contains whitespace or a control character (a trailing newline?)")
		}
	}
	return nil
}

// NewStaticProvider builds a provider for a fixed token.
func NewStaticProvider(name string, hosts []string, token, placeholder string) (*StaticProvider, error) {
	if err := ValidateToken(token); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if placeholder == "" {
		return nil, fmt.Errorf("%s: the placeholder is empty", name)
	}
	return &StaticProvider{name: name, hosts: hosts, token: token, placeholder: placeholder}, nil
}

// NewGitHubProvider is the GitHub token provider: git over https
// (github.com) and the API that gh uses (api.github.com).
func NewGitHubProvider(token, placeholder string) (*StaticProvider, error) {
	return NewStaticProvider("github", []string{"github.com:443", "api.github.com:443"}, token, placeholder)
}

func (p *StaticProvider) Name() string        { return p.name }
func (p *StaticProvider) Hosts() []string     { return p.hosts }
func (p *StaticProvider) Placeholder() string { return p.placeholder }
func (p *StaticProvider) Credential(context.Context) (string, error) {
	return p.token, nil
}

// CredentialError reports that a provider couldn't produce its credential.
// The proxy answers 502 for it. Error() deliberately leaves out Err: it may
// carry details of the secret's source.
type CredentialError struct {
	Provider string
	Err      error
}

func (e *CredentialError) Error() string { return e.Provider + ": credential unavailable" }
func (e *CredentialError) Unwrap() error { return e.Err }

// Injector is a Server.OnRequest hook that swaps providers' placeholders for
// their credentials.
type Injector struct {
	Providers []Provider
	// Logf receives one audit line per injection or refusal; nil discards them.
	// Lines name the provider and host only: never the secret, the placeholder
	// or the query string.
	Logf func(format string, args ...any)
}

func (in *Injector) logf(format string, args ...any) {
	if in.Logf != nil {
		in.Logf(format, args...)
	}
}

// OnRequest implements Server.OnRequest. For each provider:
//   - target not bound to it: a request carrying its placeholder anywhere is
//     refused (the credential is never valid there);
//   - target bound to it: the placeholder is replaced in the Authorization
//     header; one anywhere else is refused;
//   - no placeholder: the request is left untouched.
func (in *Injector) OnRequest(r *http.Request, target string) error {
	for _, p := range in.Providers {
		ph := p.Placeholder()
		if !bound(p, target) {
			if carries(r, ph, true) {
				in.logf("oc-proxy: provider %s: its placeholder was sent to %s, which it isn't bound to; refused", p.Name(), target)
				return fmt.Errorf("the %s credential isn't valid at %s", p.Name(), target)
			}
			continue
		}
		if carries(r, ph, false) {
			in.logf("oc-proxy: provider %s: its placeholder was sent outside the Authorization header to %s; refused", p.Name(), target)
			return fmt.Errorf("the %s credential may only be sent in the Authorization header", p.Name())
		}
		auth := r.Header.Get("Authorization")
		if !authCarries(auth, ph) {
			continue
		}
		secret, err := p.Credential(r.Context())
		if err != nil {
			in.logf("oc-proxy: provider %s: credential unavailable for %s", p.Name(), target)
			return &CredentialError{Provider: p.Name(), Err: err}
		}
		r.Header.Set("Authorization", substitute(auth, ph, secret))
		in.logf("oc-proxy: provider %s: credential injected for %s", p.Name(), target)
	}
	return nil
}

func bound(p Provider, target string) bool {
	for _, h := range p.Hosts() {
		if strings.EqualFold(h, target) {
			return true
		}
	}
	return false
}

// carries reports whether the placeholder appears in the request's path,
// query (also percent-decoded) or headers. For the Authorization header it
// looks at every value with includeAuth, and otherwise only at the values after
// the first: the first is the one an Injector substitutes in.
func carries(r *http.Request, ph string, includeAuth bool) bool {
	if strings.Contains(r.URL.EscapedPath(), ph) || strings.Contains(r.URL.Path, ph) || strings.Contains(r.URL.RawQuery, ph) {
		return true
	}
	if q, err := url.QueryUnescape(r.URL.RawQuery); err == nil && strings.Contains(q, ph) {
		return true
	}
	for k, vs := range r.Header {
		if k == "Authorization" {
			if !includeAuth && len(vs) > 0 {
				vs = vs[1:]
			}
			for _, v := range vs {
				if authCarries(v, ph) {
					return true
				}
			}
			continue
		}
		for _, v := range vs {
			if strings.Contains(v, ph) {
				return true
			}
		}
	}
	return false
}

func authCarries(auth, ph string) bool {
	if auth == "" {
		return false
	}
	if raw, ok := basicPayload(auth); ok {
		return bytes.Contains(raw, []byte(ph))
	}
	return strings.Contains(auth, ph)
}

// basicPayload decodes a Basic Authorization value.
func basicPayload(auth string) ([]byte, bool) {
	scheme, rest, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	return raw, err == nil
}

// substitute replaces ph with secret in an Authorization value, re-encoding
// Basic credentials.
func substitute(auth, ph, secret string) string {
	if raw, ok := basicPayload(auth); ok {
		swapped := bytes.ReplaceAll(raw, []byte(ph), []byte(secret))
		return "Basic " + base64.StdEncoding.EncodeToString(swapped)
	}
	return strings.ReplaceAll(auth, ph, secret)
}
