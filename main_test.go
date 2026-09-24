package main

import (
	"errors"
	"testing"

	"github.com/df3l0p/oc/internal/harness"
)

func TestValidateRejectsSandboxOnlyFlagsWithoutSandbox(t *testing.T) {
	for _, cfg := range []cliConfig{{image: "x"}, {build: true}} {
		if err := cfg.validate(); err == nil {
			t.Errorf("validate(%+v) = nil, want an error", cfg)
		}
	}
	for _, cfg := range []cliConfig{{}, {sandbox: true}, {sandbox: true, image: "x", build: true}} {
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
