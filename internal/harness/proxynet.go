// internal/harness/proxynet.go
package harness

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

const proxyListenAddr = "8888"

// defaultAllow is what every sandbox may reach besides its llama-server:
// package installs, git over https from GitHub, and the model catalog
// opencode fetches at startup. Everything else is blocked by the proxy.
var defaultAllow = []string{
	"registry.npmjs.org:443",
	"github.com:443",
	"models.dev:443",
}

// proxyNet is one session's dedicated egress path: an --internal Docker
// network only the proxy container can escape (it also joins the default
// bridge, giving it — and only it — a route to the host and the internet),
// plus the sandbox container's HTTP_PROXY/HTTPS_PROXY target.
type proxyNet struct {
	network    string
	container  string
	policyFile string
}

// startProxyNet creates this session's network and proxy container. hostIP
// is the address the proxy container uses to reach the host (the same value
// Sandbox.hostIP already computes for host.docker.internal). allow is the
// initial policy's allow-list.
func startProxyNet(image, hostIP string, allow []string) (*proxyNet, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("generating proxy network name: %w", err)
	}
	id := fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(suffix[:]))
	p := &proxyNet{network: "oc-net-" + id, container: "oc-proxy-" + id}

	if err := exec.Command("docker", "network", "create", "--internal", p.network).Run(); err != nil {
		return nil, fmt.Errorf("creating proxy network: %w", err)
	}

	policyPath, err := writePolicyFile(allow)
	if err != nil {
		p.stop()
		return nil, err
	}
	p.policyFile = policyPath

	runArgs := []string{
		"run", "-d", "--name", p.container,
		"--add-host", containerHost + ":" + hostIP,
		"-v", policyPath + ":/etc/oc-proxy/policy.json:ro",
		image,
	}
	if err := exec.Command("docker", runArgs...).Run(); err != nil {
		p.stop()
		return nil, fmt.Errorf("starting proxy container: %w", err)
	}

	if err := exec.Command("docker", "network", "connect", p.network, p.container).Run(); err != nil {
		p.stop()
		return nil, fmt.Errorf("connecting proxy container to its network: %w", err)
	}

	return p, nil
}

// writePolicyFile renders allow as the proxy's policy JSON to a private temp
// file, returning its path. The caller owns cleanup.
func writePolicyFile(allow []string) (string, error) {
	if allow == nil {
		allow = []string{}
	}
	raw, err := json.Marshal(struct {
		Allow []string `json:"allow"`
	}{allow})
	if err != nil {
		return "", fmt.Errorf("encoding proxy policy: %w", err)
	}
	f, err := os.CreateTemp("", "oc-proxy-policy-*.json")
	if err != nil {
		return "", fmt.Errorf("creating proxy policy file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("writing proxy policy file: %w", err)
	}
	return f.Name(), nil
}

// proxyURL is what the sandbox container should set HTTP_PROXY/HTTPS_PROXY
// to, resolving p.container by name over the shared network.
func (p *proxyNet) proxyURL() string {
	return "http://" + p.container + ":" + proxyListenAddr
}

// stop tears down the proxy container, then its network, in that order —
// the reverse of creation — plus a backstop for either failing, mirroring
// Sandbox.Run's existing `docker rm -f` backstop. Safe to call on a
// partially-started proxyNet.
func (p *proxyNet) stop() {
	if p.policyFile != "" {
		os.Remove(p.policyFile)
	}
	if p.container != "" {
		exec.Command("docker", "rm", "-f", p.container).Run()
	}
	if p.network != "" {
		exec.Command("docker", "network", "rm", p.network).Run()
	}
}
