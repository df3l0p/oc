package harness

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const proxyListenAddr = "8888"

// proxyReadyLine is what oc-proxy prints once its port is bound.
const proxyReadyLine = "oc-proxy: listening on"

// proxyReadyTimeout bounds how long startProxyNet waits for proxyReadyLine.
const proxyReadyTimeout = 10 * time.Second

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
	// caVolume is the per-session volume the proxy writes its trust bundle to
	// and the sandbox reads it from; empty when the proxy doesn't intercept.
	caVolume string
	// logFile is where the proxy's log (one audit line per intercepted
	// request) is followed to; empty when it isn't. It outlives the session.
	logFile string
	// logFollower is the `docker logs -f` writing logFile, and followDone is
	// closed once it has exited.
	logFollower *exec.Cmd
	followDone  chan struct{}
	// attach is the `docker attach` delivering the provider config on the proxy
	// container's stdin; nil when there is none.
	attach *exec.Cmd
}

// proxyCAMount is where the proxy container mounts caVolume. It must be the
// directory of oc-proxy's -ca-out default (/ca/ca.pem).
const proxyCAMount = "/ca"

// logFollowGrace is how long stop() lets the log follower drain after the
// proxy container is gone before killing it.
const logFollowGrace = 2 * time.Second

// startProxyNet creates this session's network and proxy container. hostIP
// is the address the proxy container uses to reach the host (the same value
// Sandbox.hostIP already computes for host.docker.internal). policy is the
// proxy's allow-list, in the proxy's policy format. With inspect the proxy
// also terminates TLS for the policy's explicit hosts: it gets a CA volume to
// write its trust bundle to, and its log is followed into a file. providers,
// when non-empty, is the line of provider config oc-proxy reads from its stdin
// (see proxy.EncodeProviders); it requires inspect, since credentials can only
// be injected into decrypted requests.
func startProxyNet(image, hostIP string, policy []byte, inspect bool, providers []byte) (*proxyNet, error) {
	if len(providers) > 0 && !inspect {
		return nil, fmt.Errorf("credential providers need TLS interception")
	}
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

	// A named volume, not --rm's anonymous one: the sandbox mounts it too, and
	// stop() removes it with the rest of the session's resources.
	if inspect {
		vol := "oc-ca-" + id
		if out, err := exec.Command("docker", "volume", "create", "--label", label, vol).CombinedOutput(); err != nil {
			p.stop()
			return nil, fmt.Errorf("creating proxy CA volume: %w: %s", err, strings.TrimSpace(string(out)))
		}
		p.caVolume = vol
	}

	policyPath, err := writePolicyFile(policy)
	if err != nil {
		p.stop()
		return nil, err
	}
	p.policyFile = policyPath

	runArgs := []string{
		// No --rm: if oc-proxy dies at startup its logs are the only clue
		// why, and stop() removes the container anyway. --pull=never:
		// prepareProxyImage already ensured the image locally; never let
		// docker fetch one from a registry behind our back.
		"run", "-d", "--pull=never",
		"--name", p.container,
		"--label", label,
		"--add-host", containerHost + ":" + hostIP,
		"-v", policyPath + ":/etc/oc-proxy/policy.txt:ro",
	}
	if inspect {
		runArgs = append(runArgs, "-v", p.caVolume+":"+proxyCAMount)
	}
	if len(providers) > 0 {
		// Keep stdin open: the provider config is attached to it below.
		runArgs = append(runArgs, "-i")
	}
	runArgs = append(runArgs, image)
	if inspect {
		runArgs = append(runArgs, "-intercept")
	}
	if len(providers) > 0 {
		runArgs = append(runArgs, "-providers-stdin")
	}
	if out, err := exec.Command("docker", runArgs...).CombinedOutput(); err != nil {
		p.stop()
		return nil, fmt.Errorf("starting proxy container: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if len(providers) > 0 {
		// The container reads its provider config from stdin before it listens.
		// Sent through docker attach so the secret is in no argv, env or file.
		// --sig-proxy=false: oc's own Ctrl+C must not be forwarded to the proxy.
		cmd := exec.Command("docker", "attach", "--sig-proxy=false", p.container)
		cmd.Stdin = bytes.NewReader(providers)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			p.stop()
			return nil, fmt.Errorf("attaching to the proxy container: %w", err)
		}
		p.attach = cmd
		go cmd.Wait()
	}

	if out, err := exec.Command("docker", "network", "connect", p.network, p.container).CombinedOutput(); err != nil {
		p.stop()
		return nil, fmt.Errorf("connecting proxy container to its network: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if err := p.waitReady(); err != nil {
		p.stop()
		return nil, err
	}
	if inspect {
		p.followLogs(id)
	}
	return p, nil
}

// followLogs writes the proxy's log to $TMPDIR/oc-proxy-<id>.log, as it
// happens, since opencode's full-screen UI owns the terminal. Best effort: a
// session without its log is still a working session, so a failure here only
// leaves logFile empty.
func (p *proxyNet) followLogs(id string) {
	path := filepath.Join(os.TempDir(), "oc-proxy-"+id+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer f.Close() // the child holds its own copy
	cmd := exec.Command("docker", "logs", "-f", p.container)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return
	}
	p.logFile = path
	p.logFollower = cmd
	p.followDone = make(chan struct{})
	go func() {
		cmd.Wait()
		close(p.followDone)
	}()
}

// waitReady blocks until oc-proxy reports it's listening, so the sandbox
// never starts against a proxy that died at startup. On failure the error
// carries the proxy's own output, the only clue why.
func (p *proxyNet) waitReady() error {
	deadline := time.Now().Add(proxyReadyTimeout)
	for {
		logs, _ := exec.Command("docker", "logs", p.container).CombinedOutput()
		if strings.Contains(string(logs), proxyReadyLine) {
			return nil
		}
		running, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", p.container).Output()
		if strings.TrimSpace(string(running)) != "true" {
			return fmt.Errorf("proxy container exited at startup: %s", strings.TrimSpace(string(logs)))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy container didn't report ready within %s: %s", proxyReadyTimeout, strings.TrimSpace(string(logs)))
		}
		time.Sleep(50 * time.Millisecond)
	}
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

// stop tears down the proxy container, then its network, then the CA volume
// — the reverse of creation — plus a backstop for any of them failing,
// mirroring Sandbox.Run's existing `docker rm -f` backstop. The log follower
// ends with the container; it's killed if it doesn't. The log file is kept.
// Safe to call on a partially-started proxyNet, and more than once.
func (p *proxyNet) stop() {
	if p.policyFile != "" {
		os.Remove(p.policyFile)
	}
	if p.container != "" {
		exec.Command("docker", "rm", "-f", p.container).Run()
	}
	if p.attach != nil && p.attach.Process != nil {
		p.attach.Process.Kill()
	}
	if p.followDone != nil {
		select {
		case <-p.followDone:
		case <-time.After(logFollowGrace):
			p.logFollower.Process.Kill()
		}
	}
	if p.network != "" {
		exec.Command("docker", "network", "rm", p.network).Run()
	}
	if p.caVolume != "" {
		exec.Command("docker", "volume", "rm", p.caVolume).Run()
	}
}
