package llamaserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestDiscoverModelsAllowsEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(modelsResponse{})
	}))
	defer srv.Close()

	ids, err := DiscoverModels(srv.URL)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("got %v, want no models", ids)
	}
}

func TestServerArgsHasNoModelFlag(t *testing.T) {
	command := []string{"llama", "serve"}
	got := serverArgs(command, Options{Host: "127.0.0.1", Port: 8080})
	want := []string{"serve", "--host", "127.0.0.1", "--port", "8080"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("serverArgs = %v, want %v", got, want)
	}
	if len(command) != 2 {
		t.Errorf("serverArgs mutated its input: %v", command)
	}
}

func TestResolveModel(t *testing.T) {
	ids := []string{
		"TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF:Q4_K_M",
		"unsloth/Qwen3.8-27B-GGUF:Q8_0",
		"unsloth/Qwen3.8-27B-GGUF:Q4_K_M",
	}
	tests := []struct {
		req  string
		want string
		ok   bool
	}{
		{"unsloth/Qwen3.8-27B-GGUF:Q8_0", "unsloth/Qwen3.8-27B-GGUF:Q8_0", true},
		{"unsloth/Qwen3.8-27B-GGUF", "unsloth/Qwen3.8-27B-GGUF:Q4_K_M", true},
		{"unsloth/Qwen3.8-27B-GGUF:Q2_K", "", false},
		{"unsloth/Other-GGUF", "", false},
	}
	for _, tt := range tests {
		got, ok := ResolveModel(ids, tt.req)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ResolveModel(%q) = %q, %v; want %q, %v", tt.req, got, ok, tt.want, tt.ok)
		}
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

	// Simulate another live oc process: any other running pid will do.
	other := filepath.Join(registryDir(testPort), fmt.Sprintf("%d", os.Getppid()))
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(other)

	if !OtherSessionsActive(testPort) {
		t.Error("expected another registrant to be detected")
	}
}

func TestOtherSessionsActiveIgnoresAndPrunesDeadRegistrants(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(registryDir(testPort)) })

	unregister, err := Register(testPort)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	defer unregister()

	// A pid that has exited: start a short-lived process and wait for it.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(registryDir(testPort), fmt.Sprintf("%d", cmd.Process.Pid))
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if OtherSessionsActive(testPort) {
		t.Error("a registrant whose process has exited must not count as active")
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("expected stale registration to be pruned, stat err = %v", err)
	}
}

func TestStopRecordedServerStopsProcessAndClearsRecord(t *testing.T) {
	const port = 65534
	t.Cleanup(func() { os.Remove(serverPIDPath(port)) })

	// A stand-in server in its own process group, named like llama so the
	// safety check on the recorded pid accepts it.
	dir := t.TempDir()
	fake := filepath.Join(dir, "llama-fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fake)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { cmd.Wait(); close(waited) }()

	if err := RecordServer(port, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := StopRecordedServer(port); err != nil {
		t.Fatalf("StopRecordedServer: %v", err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("recorded server was not stopped")
	}
	if _, err := os.Stat(serverPIDPath(port)); !os.IsNotExist(err) {
		t.Errorf("expected pid record removed, stat err = %v", err)
	}
}

func TestStopRecordedServerWithoutRecordIsNoop(t *testing.T) {
	if err := StopRecordedServer(65533); err != nil {
		t.Errorf("expected no error without a record, got %v", err)
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
