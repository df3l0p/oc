package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeDocker puts a `docker` shell script on PATH that appends each
// invocation's argv (one line per call) to the returned log file. exitFor maps
// an argv prefix (e.g. "build" or "image inspect oc-sandbox-") to the exit
// status it should return, the longest matching prefix winning; anything not
// listed exits 0.
func fakeDocker(t *testing.T, exitFor map[string]int) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	prefixes := make([]string, 0, len(exitFor))
	for prefix := range exitFor {
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	var cases strings.Builder
	for _, prefix := range prefixes {
		fmt.Fprintf(&cases, "  \"%s\"*) exit %d ;;\n", prefix, exitFor[prefix])
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\ncase \"$*\" in\n%s  *) exit 0 ;;\nesac\n", logPath, cases.String())
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func calls(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestContainerBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080": "http://host.docker.internal:8080",
		"http://localhost:9000": "http://host.docker.internal:9000",
		"http://0.0.0.0:8080":   "http://host.docker.internal:8080",
	} {
		got, err := containerBaseURL(in)
		if err != nil {
			t.Fatalf("containerBaseURL(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("containerBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSandboxConfigureGeneratesRewrittenCopyAndLeavesHostConfigAlone(t *testing.T) {
	hostConfig := filepath.Join(t.TempDir(), "opencode.jsonc")
	const original = `{"theme": "dark", "provider": {"other": {"name": "x"}}}`
	if err := os.WriteFile(hostConfig, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSandbox(Options{Sandbox: true}, hostConfig)

	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m1"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { os.Remove(s.generated) })

	if raw, _ := os.ReadFile(hostConfig); string(raw) != original {
		t.Errorf("host config was modified: %s", raw)
	}
	info, err := os.Stat(s.generated)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("generated config mode = %v, want 0600", info.Mode().Perm())
	}

	raw, _ := os.ReadFile(s.generated)
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("generated config is not valid JSON: %v\n%s", err, raw)
	}
	if got["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", got["theme"])
	}
	providers := got["provider"].(map[string]interface{})
	if _, ok := providers["other"]; !ok {
		t.Errorf("other provider not preserved: %v", providers)
	}
	block := providers["llama-cpp"].(map[string]interface{})
	if u := block["options"].(map[string]interface{})["baseURL"]; u != "http://host.docker.internal:8080/v1" {
		t.Errorf("baseURL = %v, want host.docker.internal", u)
	}
}

func TestSandboxConfigureWithMissingHostConfig(t *testing.T) {
	s := newSandbox(Options{Sandbox: true}, filepath.Join(t.TempDir(), "absent.jsonc"))
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m1"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	os.Remove(s.generated)
}

func TestSandboxRunArgs(t *testing.T) {
	s := newSandbox(Options{Sandbox: true}, "")
	s.image = "oc-sandbox-default:abc123" // as set by Prepare
	s.generated = "/tmp/gen.jsonc"

	args := s.runArgs("oc-1-aa", "/work/proj", "llama-cpp", "m1", false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm --pull=never -i --name oc-1-aa",
		"--add-host host.docker.internal:host-gateway",
		"-v /work/proj:/workspace",
		"-v /tmp/gen.jsonc:/etc/oc/opencode.jsonc:ro",
		"-e OPENCODE_CONFIG=/etc/oc/opencode.jsonc",
		"oc-sandbox-default:abc123 -m llama-cpp/m1 .",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, " -it ") {
		t.Errorf("-t must not be set without a TTY: %s", joined)
	}
	if got := strings.Join(s.runArgs("n", "/d", "p", "m", true), " "); !strings.Contains(got, " -it ") {
		t.Errorf("expected -it with a TTY: %s", got)
	}
}

func TestSandboxRunUsesUniqueContainerNames(t *testing.T) {
	logPath := fakeDocker(t, nil)
	var names []string
	for i := 0; i < 2; i++ {
		s := newSandbox(Options{Sandbox: true}, filepath.Join(t.TempDir(), "absent.jsonc"))
		if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m"}); err != nil {
			t.Fatal(err)
		}
		s.image = "oc-sandbox-default:test" // as set by Prepare
		if err := s.Run(t.TempDir(), "llama-cpp", "m"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if _, err := os.Stat(s.generated); !os.IsNotExist(err) {
			t.Errorf("generated config should be removed after Run, stat err = %v", err)
		}
	}
	for _, c := range calls(t, logPath) {
		f := strings.Fields(c)
		if len(f) > 3 && f[0] == "run" {
			names = append(names, f[5]) // run --rm --pull=never -i --name <name>
		}
	}
	if len(names) != 2 || names[0] == names[1] {
		t.Errorf("expected two distinct container names, got %v", names)
	}
	// Each run is followed by a backstop rm -f of its container.
	var rms int
	for _, c := range calls(t, logPath) {
		if strings.HasPrefix(c, "rm -f oc-") {
			rms++
		}
	}
	if rms != 2 {
		t.Errorf("expected 2 rm -f calls, got %d", rms)
	}
}

func TestSandboxRunRequiresConfigure(t *testing.T) {
	s := newSandbox(Options{Sandbox: true}, "")
	if err := s.Run(t.TempDir(), "p", "m"); err == nil {
		t.Fatal("expected an error when Configure wasn't called")
	}
}

func TestSandboxRunRequiresPrepare(t *testing.T) {
	logPath := fakeDocker(t, nil)
	s := newSandbox(Options{Sandbox: true}, filepath.Join(t.TempDir(), "absent.jsonc"))
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatal(err)
	}
	err := s.Run(t.TempDir(), "llama-cpp", "m")
	if err == nil || !strings.Contains(err.Error(), "Prepare") {
		t.Fatalf("Run error = %v, want one saying Prepare must be called", err)
	}
	if got := calls(t, logPath); len(got) != 0 {
		t.Errorf("docker must not be invoked without an image, got %q", got)
	}
	if _, err := os.Stat(s.generated); !os.IsNotExist(err) {
		t.Errorf("generated config must still be removed on this error path, stat err = %v", err)
	}
}

func TestSandboxAvailableNeedsDockerNotOpencode(t *testing.T) {
	s := newSandbox(Options{Sandbox: true}, "")

	t.Setenv("PATH", t.TempDir())
	if err := s.Available(); err == nil || !strings.Contains(err.Error(), "docker not found") {
		t.Errorf("expected docker-not-found error, got %v", err)
	}

	fakeDocker(t, nil) // docker present, opencode still absent from PATH
	if err := s.Available(); err != nil {
		t.Errorf("Available with docker on PATH: %v", err)
	}
}

func TestSandboxAvailableReportsUnreachableDaemon(t *testing.T) {
	fakeDocker(t, map[string]int{"info": 1})
	s := newSandbox(Options{Sandbox: true}, "")
	if err := s.Available(); err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Errorf("expected a daemon error, got %v", err)
	}
}
