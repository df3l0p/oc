package harness

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/df3l0p/oc/images"
	"github.com/df3l0p/oc/internal/opencodeconfig"
)

const (
	// containerHost is how a container reaches the host's network namespace.
	// Docker Desktop provides it; on Linux runArgs adds it with --add-host.
	containerHost = "host.docker.internal"
	// containerConfigPath is where the generated config is mounted. It's kept
	// out of the agent's own config directory so docker doesn't create that
	// directory root-owned inside the container.
	containerConfigPath = "/etc/oc/opencode.jsonc"
	containerWorkdir    = "/workspace"
	containerHome       = "/home/oc"
)

// Sandbox runs opencode in a Docker container. The container reaches the
// host's llama-server via host.docker.internal, mounts the working directory,
// and uses a generated copy of the host's opencode config with the provider
// baseURL rewritten to be container-reachable; the host config itself is never
// modified. It needs docker, not opencode, on the host.
type Sandbox struct {
	// name selects the bundled image (see package images).
	name string
	// image is the tag sessions run, set by Prepare.
	image string
	build bool
	// hostConfig is the user's opencode config, used as the base of the
	// generated one.
	hostConfig string
	// generated is the temp file written by Configure and mounted into the
	// container; removed when Run returns.
	generated string
	// hostIP is what containerHost resolves to inside the container: the
	// bridge gateway IP that llama-server listens on (set by BindHost), or
	// host-gateway when Docker provides the route itself.
	hostIP string
}

// hostOS is runtime.GOOS, replaceable in tests.
var hostOS = runtime.GOOS

// isLocalIP reports whether ip is assigned to one of this machine's
// interfaces, i.e. whether a server here can listen on it. Replaceable in
// tests.
var isLocalIP = func(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func newSandbox(opts Options, hostConfig string) *Sandbox {
	name := opts.Image
	if name == "" {
		name = images.Default
	}
	return &Sandbox{name: name, build: opts.Build, hostConfig: hostConfig, hostIP: "host-gateway"}
}

// BindHost returns the address llama-server should listen on so the container
// can reach it without exposing it to the network.
//
// Where Docker runs in a VM (any macOS, Docker Desktop) it forwards
// host.docker.internal to the host's loopback, so 127.0.0.1 is enough. With a
// native Linux engine the container's route to the host is the default
// bridge's gateway IP, so that is the address to listen on: it isn't reachable
// from outside the machine. The container is then pointed at that same IP, so
// the two can't disagree.
func (s *Sandbox) BindHost() (string, error) {
	if hostOS == "darwin" {
		return "127.0.0.1", nil
	}
	out, err := exec.Command("docker", "info", "--format", "{{.OperatingSystem}}").Output()
	if err != nil {
		return "", fmt.Errorf("querying docker info: %w", err)
	}
	if strings.Contains(string(out), "Docker Desktop") {
		return "127.0.0.1", nil
	}

	out, err = exec.Command("docker", "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output()
	if err != nil {
		return "", fmt.Errorf("querying docker's bridge network: %w (pass -host to choose the listen address yourself)", err)
	}
	for _, field := range strings.Fields(string(out)) {
		if ip := net.ParseIP(field); ip != nil && ip.To4() != nil {
			// With rootless Docker or a remote daemon the bridge lives in
			// another network namespace, so its gateway isn't an address
			// llama-server could bind here.
			if !isLocalIP(ip) {
				return "", fmt.Errorf("docker's bridge gateway %s isn't an address of this machine (rootless or remote docker?); pass -host to choose the listen address yourself", field)
			}
			s.hostIP = field
			return field, nil
		}
	}
	return "", fmt.Errorf("couldn't determine docker's bridge gateway IP (pass -host to choose the listen address yourself)")
}

func (s *Sandbox) Available() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH (required for -sandbox): %w", err)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return fmt.Errorf("docker is installed but its daemon isn't reachable (is it running?): %w", err)
	}
	return nil
}

// Prepare makes sure the selected sandbox image exists locally. oc never pulls
// its images from a registry (supply-chain hygiene): it uses a local image if
// present, and otherwise builds it from the bundled Dockerfile, failing if the
// build fails. Every selectable image is a layer on the internal base image,
// which is ensured first. With -build both are rebuilt even if they exist.
//
// Images are tagged oc-sandbox-<name>:<hash>, the hash covering the Dockerfile
// (and, for a layer, its base's tag), so a changed Dockerfile builds a new image
// and an unchanged one is reused. An unknown name fails before docker is used.
func (s *Sandbox) Prepare() error {
	dockerfile, err := images.Read(s.name)
	if err != nil {
		return err
	}
	base, err := images.ReadBase()
	if err != nil {
		return err
	}

	baseTag := imageTag(images.Base, base, "")
	if err := s.ensure(baseTag, base, ""); err != nil {
		return err
	}

	tag := imageTag(s.name, dockerfile, baseTag)
	if err := s.ensure(tag, dockerfile, baseTag); err != nil {
		return err
	}
	s.image = tag
	return nil
}

// ensure builds tag from dockerfile unless it exists locally (or -build was
// given). A non-empty baseTag is passed as the OC_BASE build arg. -build also
// skips docker's layer cache: the Dockerfiles' apt and npm steps are unpinned,
// so cached layers would keep serving old packages and an old opencode.
func (s *Sandbox) ensure(tag string, dockerfile []byte, baseTag string) error {
	if !s.build && exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "oc: building %s\n", tag)
	args := []string{"build", "-t", tag, "-"}
	if baseTag != "" {
		args = layerBuildArgs(tag, baseTag)
	}
	if s.build {
		args = append([]string{"build", "--no-cache"}, args[1:]...)
	}
	// A Dockerfile on stdin means no build context, which is all it needs.
	cmd := exec.Command("docker", args...)
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", tag, err)
	}
	return nil
}

// imageTag names an image built from dockerfile. baseTag is the tag of the
// image it layers on, or "" for the base image itself.
func imageTag(name string, dockerfile []byte, baseTag string) string {
	sum := sha256.New()
	sum.Write(dockerfile)
	sum.Write([]byte{0})
	sum.Write([]byte(baseTag))
	return "oc-sandbox-" + name + ":" + hex.EncodeToString(sum.Sum(nil))[:12]
}

// layerBuildArgs is the docker argv that builds a layer's Dockerfile, read from
// stdin, with the base image's tag as its OC_BASE build arg.
func layerBuildArgs(tag, baseTag string) []string {
	return []string{"build", "--build-arg", "OC_BASE=" + baseTag, "-t", tag, "-"}
}

// Configure writes a copy of the host opencode config, with the provider
// pointed at baseURL as seen from inside a container, to a private temp file.
func (s *Sandbox) Configure(providerKey, baseURL string, modelIDs []string) error {
	containerURL, err := containerBaseURL(baseURL)
	if err != nil {
		return err
	}
	out, err := opencodeconfig.Render(s.hostConfig, providerKey, opencodeProvider(containerURL, modelIDs))
	if err != nil {
		return err
	}
	// CreateTemp creates the file 0600; the container runs as the same uid.
	f, err := os.CreateTemp("", "oc-sandbox-config-*.jsonc")
	if err != nil {
		return fmt.Errorf("creating sandbox config: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("writing %s: %w", f.Name(), err)
	}
	s.generated = f.Name()
	return nil
}

func (s *Sandbox) Run(dir, providerKey, modelID string) error {
	if s.generated == "" {
		return fmt.Errorf("sandbox: Configure must be called before Run")
	}
	defer os.Remove(s.generated)
	if s.image == "" {
		return fmt.Errorf("sandbox: Prepare must be called before Run")
	}

	// Unique per invocation so parallel sessions never collide, whatever port
	// they share.
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("generating container name: %w", err)
	}
	name := fmt.Sprintf("oc-%d-%s", os.Getpid(), hex.EncodeToString(suffix[:]))

	// Backstop for a container left behind if docker's client dies without the
	// daemon noticing; --rm covers the normal path.
	defer exec.Command("docker", "rm", "-f", name).Run()

	cmd := exec.Command("docker", s.runArgs(name, dir, providerKey, modelID, stdinIsTTY())...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runArgs builds the docker argv for one session.
func (s *Sandbox) runArgs(name, dir, providerKey, modelID string, tty bool) []string {
	flags := "-i"
	if tty {
		flags = "-it"
	}
	return []string{
		// --pull=never: Prepare already ensured the image locally; never let
		// docker fetch one from a registry behind our back.
		"run", "--rm", "--pull=never", flags,
		"--name", name,
		"--add-host", containerHost + ":" + s.hostIP,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "HOME=" + containerHome,
		"-e", "OPENCODE_CONFIG=" + containerConfigPath,
		"-v", dir + ":" + containerWorkdir,
		"-v", s.generated + ":" + containerConfigPath + ":ro",
		"-w", containerWorkdir,
		s.image,
		// -m selects the model for this session only, as in host mode.
		"-m", providerKey + "/" + modelID, ".",
	}
}

// containerBaseURL rewrites a host-side URL (e.g. http://127.0.0.1:8080) to
// the address the container uses to reach the same server.
func containerBaseURL(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing base URL %q: %w", baseURL, err)
	}
	port := u.Port() // read before Host is overwritten
	u.Host = containerHost
	if port != "" {
		u.Host = net.JoinHostPort(containerHost, port)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// stdinIsTTY reports whether stdin is a terminal. A ModeCharDevice check isn't
// enough (it's also true for /dev/null), and the isatty ioctl is
// platform-specific without golang.org/x/term, so ask the shell's test -t.
func stdinIsTTY() bool {
	cmd := exec.Command("sh", "-c", "test -t 0")
	cmd.Stdin = os.Stdin
	return cmd.Run() == nil
}
