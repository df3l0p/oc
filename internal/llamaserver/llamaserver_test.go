package llamaserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOptionsBaseURL(t *testing.T) {
	opts := Options{Host: "127.0.0.1", Port: 8080}
	want := "http://127.0.0.1:8080"
	if got := opts.BaseURL(); got != want {
		t.Errorf("BaseURL() = %q, want %q", got, want)
	}
}

func TestIsHealthyFalseWhenNothingListening(t *testing.T) {
	if IsHealthy("http://127.0.0.1:1") {
		t.Error("expected IsHealthy to be false for an unreachable server")
	}
}

// testPort is a value unlikely to collide with a real llama-server instance
// on this machine, since the registry is keyed by port under os.TempDir().
const testPort = 65535

func TestRegisterAloneMeansNoOtherSessions(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(registryDir(testPort)) })

	unregister, err := Register(testPort)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	defer unregister()

	if OtherSessionsActive(testPort) {
		t.Error("expected no other sessions when this is the only registrant")
	}
}

func TestOtherSessionsActiveDetectsAnotherRegistrant(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(registryDir(testPort)) })

	unregister, err := Register(testPort)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	defer unregister()

	// Simulate another oc process registering with a different pid.
	other := filepath.Join(registryDir(testPort), "999999")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(other)

	if !OtherSessionsActive(testPort) {
		t.Error("expected another registrant to be detected")
	}
}

func TestUnregisterRemovesSelf(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(registryDir(testPort)) })

	unregister, err := Register(testPort)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	unregister()

	entries, err := os.ReadDir(registryDir(testPort))
	if err != nil {
		t.Fatalf("reading registry dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty registry after unregister, got %v", entries)
	}
}
