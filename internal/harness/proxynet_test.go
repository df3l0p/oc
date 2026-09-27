package harness

import (
	"os"
	"strings"
	"testing"
)

func TestStartProxyNetCreatesNetworkThenContainerThenConnectsBridge(t *testing.T) {
	logPath := fakeDocker(t, nil)

	p, err := startProxyNet("oc-proxy:test", "172.17.0.1", []string{"host.docker.internal:8080"})
	if err != nil {
		t.Fatalf("startProxyNet: %v", err)
	}
	defer p.stop()

	// The same session id backs the network name, the container name, and
	// the oc.session label on both, so an operator can correlate all three
	// (and docker ps/network ls --filter by it).
	id := strings.TrimPrefix(p.network, "oc-net-")
	if id == "" || id == p.network {
		t.Fatalf("could not derive session id from network name %q", p.network)
	}
	label := "oc.session=" + id

	calls := calls(t, logPath)
	var netCreateIdx, runIdx, connectIdx = -1, -1, -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "network create --internal "):
			netCreateIdx = i
			if !strings.Contains(c, "--label "+label) {
				t.Errorf("network create args missing the session label: %s", c)
			}
			if !strings.Contains(c, p.network) {
				t.Errorf("network create args missing the network name: %s", c)
			}
		case strings.HasPrefix(c, "run -d "):
			runIdx = i
			if !strings.Contains(c, "--name "+p.container) {
				t.Errorf("proxy run args missing --name %s: %s", p.container, c)
			}
			if !strings.Contains(c, "--rm") {
				t.Errorf("proxy run args missing --rm: %s", c)
			}
			if !strings.Contains(c, "--pull=never") {
				t.Errorf("proxy run args missing --pull=never: %s", c)
			}
			if !strings.Contains(c, "--label "+label) {
				t.Errorf("proxy run args missing the session label: %s", c)
			}
			if !strings.Contains(c, "--add-host host.docker.internal:172.17.0.1") {
				t.Errorf("proxy run args missing the host add-host: %s", c)
			}
			if !strings.Contains(c, "oc-proxy:test") {
				t.Errorf("proxy run args missing the image: %s", c)
			}
		case strings.HasPrefix(c, "network connect "+p.network+" "+p.container):
			connectIdx = i
		}
	}
	if netCreateIdx < 0 || runIdx < 0 || connectIdx < 0 {
		t.Fatalf("expected network create, run, and network connect calls, got %v", calls)
	}
	if !(netCreateIdx < runIdx && runIdx < connectIdx) {
		t.Errorf("expected create, then run, then connect, in that order; got indices %d %d %d", netCreateIdx, runIdx, connectIdx)
	}
}

func TestStartProxyNetWritesAnAllowListedPolicyFile(t *testing.T) {
	fakeDocker(t, nil)
	p, err := startProxyNet("oc-proxy:test", "172.17.0.1", []string{"host.docker.internal:8080"})
	if err != nil {
		t.Fatalf("startProxyNet: %v", err)
	}
	defer p.stop()
	if p.policyFile == "" {
		t.Fatal("expected a generated policy file path")
	}
	raw, err := os.ReadFile(p.policyFile)
	if err != nil {
		t.Fatalf("reading generated policy file: %v", err)
	}
	if !strings.Contains(string(raw), "host.docker.internal:8080") {
		t.Errorf("policy file missing the expected allow entry: %s", raw)
	}
}

func TestProxyNetStopTearsDownContainerThenNetwork(t *testing.T) {
	logPath := fakeDocker(t, nil)
	p, err := startProxyNet("oc-proxy:test", "172.17.0.1", nil)
	if err != nil {
		t.Fatalf("startProxyNet: %v", err)
	}
	p.stop()

	calls := calls(t, logPath)
	var rmIdx, netRmIdx = -1, -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "rm -f "+p.container):
			rmIdx = i
		case strings.HasPrefix(c, "network rm "+p.network):
			netRmIdx = i
		}
	}
	if rmIdx < 0 || netRmIdx < 0 {
		t.Fatalf("expected container rm and network rm calls, got %v", calls)
	}
	if rmIdx > netRmIdx {
		t.Errorf("container must be removed before its network, got indices %d, %d", rmIdx, netRmIdx)
	}
}

func TestStartProxyNetCleansUpTheNetworkWhenTheContainerFailsToStart(t *testing.T) {
	logPath := fakeDocker(t, map[string]int{"run -d": 1})
	if _, err := startProxyNet("oc-proxy:test", "172.17.0.1", nil); err == nil {
		t.Fatal("expected an error when the proxy container fails to start")
	}
	var created, removed string
	for _, c := range calls(t, logPath) {
		if strings.HasPrefix(c, "network create --internal ") {
			f := strings.Fields(c)
			created = f[len(f)-1] // network create --internal --label <label> <network>
		}
		if strings.HasPrefix(c, "network rm ") {
			removed = strings.TrimPrefix(c, "network rm ")
		}
	}
	if created == "" || removed != created {
		t.Errorf("network %q was created but %q was removed; a failed start must not leak it", created, removed)
	}
}

// TestStartProxyNetSurfacesDockerStderr checks that a docker failure's own
// stderr output ends up in the returned error, not just docker's bare exit
// status: that's the only clue an operator sees, since oc never runs docker
// with its own stdout/stderr wired to the terminal here.
func TestStartProxyNetSurfacesDockerStderr(t *testing.T) {
	fakeDockerOutput(t,
		map[string]int{"network create": 1},
		map[string]string{"network create": "Error response from daemon: pool overlaps with other one on this address space"},
	)
	_, err := startProxyNet("oc-proxy:test", "172.17.0.1", nil)
	if err == nil {
		t.Fatal("expected an error when network create fails")
	}
	if !strings.Contains(err.Error(), "pool overlaps with other one on this address space") {
		t.Errorf("error = %v, want it to include docker's own output", err)
	}
}

func TestSandboxAllowListIsDefaultsPlusUpstream(t *testing.T) {
	s := newSandbox(Options{Sandbox: true}, "")
	s.upstream = "host.docker.internal:8080"
	got := strings.Join(s.allowList(), " ")
	for _, want := range []string{"host.docker.internal:8080", "registry.npmjs.org:443", "github.com:443", "models.dev:443"} {
		if !strings.Contains(got, want) {
			t.Errorf("allow-list %q missing %q", got, want)
		}
	}
	// allowList must not grow defaultAllow's backing array across sessions.
	s2 := newSandbox(Options{Sandbox: true}, "")
	s2.upstream = "host.docker.internal:9090"
	s2.allowList()
	if strings.Contains(strings.Join(s.allowList(), " "), "9090") {
		t.Error("one session's upstream leaked into another's allow-list")
	}
}
