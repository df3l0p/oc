package llamaserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestFindCommandNotFoundOnEmptyPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := FindCommand()
	if err == nil {
		t.Fatal("expected an error when neither binary is on PATH")
	}
	if !strings.Contains(err.Error(), "llama") || !strings.Contains(err.Error(), "llama-server") {
		t.Errorf("expected error to mention both binary names, got: %v", err)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(err.Error(), "brew") {
		t.Errorf("expected macOS error to mention brew, got: %v", err)
	}
}

func TestDiscoverModelsReturnsAllIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{
			Data: []struct {
				ID string `json:"id"`
			}{{ID: "model-a"}, {ID: "model-b"}},
		})
	}))
	defer srv.Close()

	ids, err := DiscoverModels(srv.URL)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	want := []string{"model-a", "model-b"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("got %v, want %v", ids, want)
	}
}

func TestDiscoverModelsErrorsOnEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{})
	}))
	defer srv.Close()

	if _, err := DiscoverModels(srv.URL); err == nil {
		t.Error("expected an error when the server reports no models")
	}
}

func TestDiscoverModelsErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := DiscoverModels(srv.URL); err == nil {
		t.Error("expected an error on a non-200 response")
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
