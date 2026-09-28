package harness

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const proxyListenAddr = "8888"

// defaultPolicy is what every sandbox may reach besides its llama-server, in
// the proxy's policy format (see proxy.ParsePolicy).
//
//go:embed policy.txt
var defaultPolicy []byte

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
// Sandbox.hostIP already computes for host.docker.internal). policy is the
// proxy's allow-list, in the proxy's policy format.
func startProxyNet(image, hostIP string, policy []byte) (*proxyNet, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("generating proxy network name: %w", err)
	}
	id := fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(suffix[:]))
	p := &proxyNet{network: "oc-net-" + id, container: "oc-proxy-" + id}
	// Same id as the network/container names, so `docker ... --filter
	// label=oc.session=<id>` (or plain `docker ps`/`network ls`) ties both
	// of one session's resources together.
	label := "oc.session=" + id

	if out, err := exec.Command("docker", "network", "create", "--internal", "--label", label, p.network).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating proxy network: %w: %s", err, strings.TrimSpace(string(out)))
	}

	policyPath, err := writePolicyFile(policy)
	if err != nil {
		p.stop()
		return nil, err
	}
	p.policyFile = policyPath

	runArgs := []string{
		// --rm: session data inside the proxy container is discarded on
		// exit, same as the sandbox container. --pull=never: prepareProxyImage
		// already ensured the image locally; never let docker fetch one from
		// a registry behind our back.
		"run", "-d", "--rm", "--pull=never",
		"--name", p.container,
		"--label", label,
		"--add-host", containerHost + ":" + hostIP,
		"-v", policyPath + ":/etc/oc-proxy/policy.txt:ro",
		image,
	}
	if out, err := exec.Command("docker", runArgs...).CombinedOutput(); err != nil {
		p.stop()
		return nil, fmt.Errorf("starting proxy container: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if out, err := exec.Command("docker", "network", "connect", p.network, p.container).CombinedOutput(); err != nil {
		p.stop()
		return nil, fmt.Errorf("connecting proxy container to its network: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return p, nil
}

// writePolicyFile writes policy to a private temp file, returning its path.
// The caller owns cleanup.
func writePolicyFile(policy []byte) (string, error) {
	f, err := os.CreateTemp("", "oc-proxy-policy-*.txt")
	if err != nil {
		return "", fmt.Errorf("creating proxy policy file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(policy); err != nil {
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
