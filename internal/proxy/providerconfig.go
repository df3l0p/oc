package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProviderConfig is one provider as oc hands it to oc-proxy on stdin.
type ProviderConfig struct {
	Name        string `json:"name"`
	Token       string `json:"token"`
	Placeholder string `json:"placeholder"`
}

type providersMessage struct {
	Providers []ProviderConfig `json:"providers"`
}

// EncodeProviders renders cfgs as the single newline-terminated JSON line
// ReadProviders expects.
func EncodeProviders(cfgs []ProviderConfig) ([]byte, error) {
	b, err := json.Marshal(providersMessage{Providers: cfgs})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ReadProviders reads one newline-terminated line holding a JSON
// providersMessage (`{"providers":[{...},{...}]}`, so any number of providers
// in that one line) and builds the providers. It stops at the newline, so the
// sender need not close the stream. Names and placeholders must be unique: the
// placeholder is how the proxy tells whose credential to inject, so a shared
// one would be ambiguous. Errors never include the line's content: it holds
// secrets.
func ReadProviders(r *bufio.Reader) ([]Provider, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, errors.New("provider config isn't newline-terminated")
		}
		return nil, fmt.Errorf("reading provider config: %w", err)
	}
	var msg providersMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, errors.New("provider config isn't valid JSON")
	}
	if len(msg.Providers) == 0 {
		return nil, errors.New("provider config lists no providers")
	}
	names, placeholders := map[string]bool{}, map[string]bool{}
	for _, c := range msg.Providers {
		if names[c.Name] {
			return nil, fmt.Errorf("provider %q is listed twice", c.Name)
		}
		if placeholders[c.Placeholder] {
			return nil, errors.New("two providers share a placeholder")
		}
		names[c.Name], placeholders[c.Placeholder] = true, true
	}
	ps := make([]Provider, 0, len(msg.Providers))
	for _, c := range msg.Providers {
		p, err := BuildProvider(c)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, nil
}

// InjectorFromStdin reads the provider config from r (see ReadProviders) and
// returns the Injector for it, logging to logf. Every host a provider is bound
// to must be listed explicitly in pol: only explicit hosts are decrypted, so a
// credential could never be injected at any other.
func InjectorFromStdin(r *bufio.Reader, pol Policy, logf func(string, ...any)) (*Injector, error) {
	providers, err := ReadProviders(r)
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		for _, h := range p.Hosts() {
			if !pol.Explicit(h) {
				return nil, fmt.Errorf("provider %s is bound to %s, which the policy doesn't list explicitly, so it would never be decrypted", p.Name(), h)
			}
		}
	}
	return &Injector{Providers: providers, Logf: logf}, nil
}

// BuildProvider turns a config entry into its Provider ("github" or "openrouter").
func BuildProvider(c ProviderConfig) (Provider, error) {
	switch c.Name {
	case "github":
		return NewGitHubProvider(c.Token, c.Placeholder)
	case "openrouter":
		return NewOpenRouterProvider(c.Token, c.Placeholder)
	default:
		return nil, fmt.Errorf("unknown provider %q", c.Name)
	}
}
