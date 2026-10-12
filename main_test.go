package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/df3l0p/rnes/internal/harness"
)

func TestValidateRejectsSandboxOnlyFlagsWithoutSandbox(t *testing.T) {
	for _, cfg := range []cliConfig{{image: "x"}, {build: true}, {allNet: true}, {noInspect: true}} {
		if err := cfg.validate(); err == nil {
			t.Errorf("validate(%+v) = nil, want an error", cfg)
		}
	}
	for _, cfg := range []cliConfig{{}, {sandbox: true}, {sandboxUpdate: true}, {sandbox: true, image: "x", build: true, allNet: true, noInspect: true}} {
		if err := cfg.validate(); err != nil {
			t.Errorf("validate(%+v) = %v, want nil", cfg, err)
		}
	}
}

// TestSandboxUpdateImpliesSandboxAndBuild checks that -sandbox-update turns
// into a from-scratch sandbox rebuild, so prepare runs and validate passes
// for the sandbox-only flags given alongside it.
func TestSandboxUpdateImpliesSandboxAndBuild(t *testing.T) {
	c := cliConfig{sandboxUpdate: true, image: "x"}
	c.applyImplications()
	if !c.sandbox || !c.build {
		t.Fatalf("applyImplications = sandbox=%v build=%v, want both true", c.sandbox, c.build)
	}
	if err := c.validate(); err != nil {
		t.Errorf("validate(%+v) = %v, want nil", c, err)
	}
	c2 := cliConfig{}
	c2.applyImplications()
	if c2.sandbox || c2.build {
		t.Errorf("applyImplications without -sandbox-update = sandbox=%v build=%v, want both false", c2.sandbox, c2.build)
	}
}

// fakeBinder is a harness that picks its own listen address.
type fakeBinder struct {
	harness.Harness
	host string
	err  error
}

func (f fakeBinder) BindHost() (string, error) { return f.host, f.err }

func TestListenHost(t *testing.T) {
	var plain harness.Harness // doesn't implement BindHoster
	tests := []struct {
		name    string
		h       harness.Harness
		host    string
		hostSet bool
		want    string
		wantErr bool
	}{
		{"explicit default host is not narrowed", fakeBinder{host: "172.17.0.1"}, defaultHost, true, defaultHost, false},
		{"host harness keeps the default", plain, defaultHost, false, defaultHost, false},
		{"container harness narrows the default", fakeBinder{host: "172.17.0.1"}, defaultHost, false, "172.17.0.1", false},
		{"container harness error is returned", fakeBinder{err: errors.New("boom")}, defaultHost, false, "", true},
		{"explicit host wins over the harness", fakeBinder{host: "172.17.0.1"}, "192.168.1.5", true, "192.168.1.5", false},
		{"explicit wildcard is honoured", fakeBinder{host: "172.17.0.1"}, "0.0.0.0", true, "0.0.0.0", false},
	}
	for _, tt := range tests {
		got, err := listenHost(tt.h, tt.host, tt.hostSet)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s: listenHost = (%q, %v), want (%q, err=%v)", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestGitHubTokenFromEnvReadsAndUnsets(t *testing.T) {
	t.Setenv("RNES_GITHUB_TOKEN", "github_pat_x")
	if got := githubTokenFromEnv(); got != "github_pat_x" {
		t.Fatalf("token = %q", got)
	}
	if v, ok := os.LookupEnv("RNES_GITHUB_TOKEN"); ok {
		t.Errorf("RNES_GITHUB_TOKEN is still set (%q): child processes would inherit it", v)
	}
}

func TestValidateRejectsAGitHubTokenWithNoInspect(t *testing.T) {
	c := cliConfig{sandbox: true, noInspect: true, githubToken: "github_pat_x"}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "-no-inspect") {
		t.Errorf("err = %v, want one naming -no-inspect", err)
	}
}

func TestValidateRejectsAnUnusableGitHubToken(t *testing.T) {
	c := cliConfig{sandbox: true, githubToken: "github_pat_x\n"}
	err := c.validate()
	if err == nil || !strings.Contains(err.Error(), "RNES_GITHUB_TOKEN") || strings.Contains(err.Error(), "github_pat_x") {
		t.Errorf("err = %v, want one naming RNES_GITHUB_TOKEN and not echoing it", err)
	}
}

func TestValidateAcceptsAGitHubTokenWithSandbox(t *testing.T) {
	if err := (cliConfig{sandbox: true, githubToken: "github_pat_x"}).validate(); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestUsageDocumentsTheGitHubTokenAndTheGhShortcut(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, want := range []string{"Environment:", "RNES_GITHUB_TOKEN", "fine-grained", "export RNES_GITHUB_TOKEN=$(gh auth token)", "-sandbox"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, buf.String())
		}
	}
}

func TestTokenWarningFlagsAnythingButAFineGrainedToken(t *testing.T) {
	for _, tok := range []string{"ghp_abc", "gho_abc", "ghu_abc", "ghs_abc", "v1.0123456789abcdef"} {
		if tokenWarning(tok) == "" {
			t.Errorf("tokenWarning(%q) is empty, want a warning", tok)
		}
	}
	if w := tokenWarning("github_pat_abc"); w != "" {
		t.Errorf("tokenWarning(fine-grained) = %q, want none", w)
	}
}

// TestSessionCredentialIsKeptOnlyForASession checks that a credential is
// dropped when no session will use it: without -sandbox, and with
// -sandbox-update, which exits before one starts.
func TestSessionCredentialIsKeptOnlyForASession(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  cliConfig
		want string
	}{
		{"no sandbox", cliConfig{}, ""},
		{"sandbox", cliConfig{sandbox: true}, "tok"},
		{"sandbox-update", cliConfig{sandbox: true, sandboxUpdate: true}, ""},
	} {
		if got := tc.cfg.sessionCredential("RNES_X", "tok"); got != tc.want {
			t.Errorf("%s: credential = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOpenRouterKeyFromEnvReadsAndUnsets(t *testing.T) {
	t.Setenv("RNES_OPENROUTER_API_KEY", "sk-or-x")
	if got := openRouterKeyFromEnv(); got != "sk-or-x" {
		t.Errorf("got %q", got)
	}
	if v, ok := os.LookupEnv("RNES_OPENROUTER_API_KEY"); ok {
		t.Errorf("RNES_OPENROUTER_API_KEY is still set (%q): child processes would inherit it", v)
	}
}

func TestValidateRejectsAnOpenRouterKeyWithNoInspect(t *testing.T) {
	err := cliConfig{sandbox: true, noInspect: true, openrouterKey: "sk-or-x"}.validate()
	if err == nil || !strings.Contains(err.Error(), "RNES_OPENROUTER_API_KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateRejectsAnUnusableOpenRouterKey(t *testing.T) {
	err := cliConfig{sandbox: true, openrouterKey: "sk-or-x\n"}.validate()
	if err == nil || !strings.Contains(err.Error(), "RNES_OPENROUTER_API_KEY") || strings.Contains(err.Error(), "sk-or-x") {
		t.Errorf("err = %v, want one naming RNES_OPENROUTER_API_KEY and not echoing it", err)
	}
}

func TestActiveProvidersListsOnlyTheSetOnes(t *testing.T) {
	if got := activeProviders(cliConfig{}); len(got) != 0 {
		t.Errorf("none set: %v", got)
	}
	got := activeProviders(cliConfig{githubToken: "a", openrouterKey: "b"})
	if strings.Join(got, ",") != "github,openrouter" {
		t.Errorf("both set: %v", got)
	}
}

func TestUsageDocumentsTheOpenRouterKey(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, want := range []string{"RNES_OPENROUTER_API_KEY", "credit limit", "-sandbox"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, buf.String())
		}
	}
}
