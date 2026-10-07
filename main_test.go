package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/df3l0p/oc/internal/harness"
)

func TestValidateRejectsSandboxOnlyFlagsWithoutSandbox(t *testing.T) {
	for _, cfg := range []cliConfig{{image: "x"}, {build: true}, {allNet: true}, {noInspect: true}} {
		if err := cfg.validate(); err == nil {
			t.Errorf("validate(%+v) = nil, want an error", cfg)
		}
	}
	for _, cfg := range []cliConfig{{}, {sandbox: true}, {sandbox: true, image: "x", build: true, allNet: true, noInspect: true}} {
		if err := cfg.validate(); err != nil {
			t.Errorf("validate(%+v) = %v, want nil", cfg, err)
		}
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
	t.Setenv("OC_GITHUB_TOKEN", "github_pat_x")
	if got := githubTokenFromEnv(); got != "github_pat_x" {
		t.Fatalf("token = %q", got)
	}
	if v, ok := os.LookupEnv("OC_GITHUB_TOKEN"); ok {
		t.Errorf("OC_GITHUB_TOKEN is still set (%q): child processes would inherit it", v)
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
	if err == nil || !strings.Contains(err.Error(), "OC_GITHUB_TOKEN") || strings.Contains(err.Error(), "github_pat_x") {
		t.Errorf("err = %v, want one naming OC_GITHUB_TOKEN and not echoing it", err)
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
	for _, want := range []string{"Environment:", "OC_GITHUB_TOKEN", "fine-grained", "export OC_GITHUB_TOKEN=$(gh auth token)", "-sandbox"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, buf.String())
		}
	}
}
