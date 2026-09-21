package main

import "testing"

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

func TestBindHost(t *testing.T) {
	tests := []struct {
		cfg  cliConfig
		want string
	}{
		{cliConfig{host: defaultHost}, defaultHost},
		{cliConfig{host: defaultHost, sandbox: true}, "0.0.0.0"},
		{cliConfig{host: "192.168.1.5", sandbox: true}, "192.168.1.5"},
	}
	for _, tt := range tests {
		if got := tt.cfg.bindHost(); got != tt.want {
			t.Errorf("bindHost(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}
