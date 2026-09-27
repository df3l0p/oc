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

	calls := calls(t, logPath)
	var netCreateIdx, runIdx, connectIdx = -1, -1, -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "network create --internal "+p.network):
			netCreateIdx = i
		case strings.HasPrefix(c, "run -d --name "+p.container):
			runIdx = i
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
			created = strings.TrimPrefix(c, "network create --internal ")
		}
		if strings.HasPrefix(c, "network rm ") {
			removed = strings.TrimPrefix(c, "network rm ")
		}
	}
	if created == "" || removed != created {
		t.Errorf("network %q was created but %q was removed; a failed start must not leak it", created, removed)
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
