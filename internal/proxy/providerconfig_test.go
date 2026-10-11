package proxy

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestEncodeProvidersIsOneNewlineTerminatedLine(t *testing.T) {
	b, err := EncodeProviders([]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "oc_placeholder_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("\n")) || bytes.Count(b, []byte("\n")) != 1 {
		t.Errorf("want exactly one trailing newline, got %q", b)
	}
}

func TestReadProvidersRoundTripsAndBuildsTheGitHubProvider(t *testing.T) {
	b, _ := EncodeProviders([]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "oc_placeholder_1"}})
	// More input may follow the line (stdin isn't closed): only one line is read.
	ps, err := ReadProviders(bufio.NewReader(bytes.NewReader(append(b, []byte("trailing")...))))
	if err != nil {
		t.Fatalf("ReadProviders: %v", err)
	}
	if len(ps) != 1 || ps[0].Name() != "github" || ps[0].Placeholder() != "oc_placeholder_1" {
		t.Fatalf("providers = %+v", ps)
	}
}

func TestReadProvidersRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":            "\n",
		"not json":         "nope\n",
		"unknown provider": `{"providers":[{"name":"gitlab","token":"t","placeholder":"p"}]}` + "\n",
		"bad token":        `{"providers":[{"name":"github","token":"a b","placeholder":"p"}]}` + "\n",
		"no providers":     `{"providers":[]}` + "\n",
		"no newline":       `{"providers":[]}`,
		"duplicate name": `{"providers":[{"name":"github","token":"a","placeholder":"p1"},` +
			`{"name":"github","token":"b","placeholder":"p2"}]}` + "\n",
		"duplicate placeholder": `{"providers":[{"name":"github","token":"a","placeholder":"p"},` +
			`{"name":"other","token":"b","placeholder":"p"}]}` + "\n",
	} {
		if _, err := ReadProviders(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReadProvidersErrorsNeverEchoTheToken(t *testing.T) {
	in := `{"providers":[{"name":"github","token":"SECRET VALUE","placeholder":"p"}]}` + "\n"
	_, err := ReadProviders(bufio.NewReader(strings.NewReader(in)))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v; must exist and not contain the token", err)
	}
}

// The duplicate checks must be what rejects these (the second entry names a
// provider that doesn't exist, so a later check would also fail them).
func TestReadProvidersNamesTheDuplicate(t *testing.T) {
	for in, want := range map[string]string{
		`{"providers":[{"name":"github","token":"a","placeholder":"p1"},{"name":"github","token":"b","placeholder":"p2"}]}` + "\n": "listed twice",
		`{"providers":[{"name":"github","token":"a","placeholder":"p"},{"name":"other","token":"b","placeholder":"p"}]}` + "\n":    "share a placeholder",
	} {
		_, err := ReadProviders(bufio.NewReader(strings.NewReader(in)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one containing %q", err, want)
		}
	}
}

func TestInjectorFromStdinBuildsTheInjectorForExplicitHosts(t *testing.T) {
	line, _ := EncodeProviders([]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "oc_placeholder_1"}})
	pol := NewPolicy([]string{"github.com:443", "api.github.com:443"})
	in, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), pol, nil)
	if err != nil {
		t.Fatalf("InjectorFromStdin: %v", err)
	}
	if len(in.Providers) != 1 || in.Providers[0].Name() != "github" {
		t.Errorf("providers = %+v", in.Providers)
	}
}

// A bound host the policy doesn't list explicitly is never decrypted, so the
// credential could never be injected there: refuse to start.
func TestInjectorFromStdinRefusesBoundHostsThePolicyDoesNotList(t *testing.T) {
	line, _ := EncodeProviders([]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "oc_placeholder_1"}})
	pol := NewPolicy([]string{"github.com:443"}) // no api.github.com
	_, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), pol, nil)
	if err == nil || !strings.Contains(err.Error(), "api.github.com:443") {
		t.Errorf("err = %v, want one naming api.github.com:443", err)
	}
	// "*" allows the host but doesn't make it explicit.
	any := Policy{Allow: map[string]bool{"github.com:443": true}, AnyPublic: true}
	if _, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), any, nil); err == nil {
		t.Error("a host allowed only by * must not count as explicit")
	}
}

func TestInjectorFromStdinPassesTheReadErrors(t *testing.T) {
	_, err := InjectorFromStdin(bufio.NewReader(strings.NewReader("nope\n")), NewPolicy(nil), nil)
	if err == nil {
		t.Error("want an error for unreadable config")
	}
}

func TestReadProvidersBuildsGitHubAndOpenRouterFromOneLine(t *testing.T) {
	b, _ := EncodeProviders([]ProviderConfig{
		{Name: "github", Token: "github_pat_x", Placeholder: "p1"},
		{Name: "openrouter", Token: "sk-or-x", Placeholder: "p2"},
	})
	ps, err := ReadProviders(bufio.NewReader(bytes.NewReader(b)))
	if err != nil || len(ps) != 2 {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
	if ps[1].Name() != "openrouter" || strings.Join(ps[1].Hosts(), ",") != "openrouter.ai:443" {
		t.Errorf("openrouter provider = %s %v", ps[1].Name(), ps[1].Hosts())
	}
}
