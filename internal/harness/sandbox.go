package harness

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/df3l0p/oc/internal/opencodeconfig"
)

// DefaultSandboxImage is the container image used by -sandbox unless
// overridden with -image.
const DefaultSandboxImage = "ghcr.io/df3l0p/oc-sandbox:latest"

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

//go:embed Dockerfile
var sandboxDockerfile []byte

// Sandbox runs opencode in a Docker container. The container reaches the
// host's llama-server via host.docker.internal, mounts the working directory,
// and uses a generated copy of the host's opencode config with the provider
// baseURL rewritten to be container-reachable; the host config itself is never
// modified. It needs docker, not opencode, on the host.
type Sandbox struct {
	image string
	build bool
	// hostConfig is the user's opencode config, used as the base of the
	// generated one.
	hostConfig string
	// generated is the temp file written by Configure and mounted into the
	// container; removed when Run returns.
	generated string
}

func newSandbox(opts Options, hostConfig string) *Sandbox {
	image := opts.Image
	if image == "" {
		image = DefaultSandboxImage
	}
	return &Sandbox{image: image, build: opts.Build, hostConfig: hostConfig}
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

// Prepare makes sure the sandbox image exists locally: with -build it builds
// from the embedded Dockerfile; otherwise it uses a local image if present,
// else pulls, and falls back to building only for the default image (whose
// registry copy may not exist yet).
func (s *Sandbox) Prepare() error {
	if s.build {
		return s.buildImage()
	}
	if exec.Command("docker", "image", "inspect", s.image).Run() == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "oc: pulling %s\n", s.image)
	pull := exec.Command("docker", "pull", s.image)
	pull.Stdout, pull.Stderr = os.Stderr, os.Stderr
	pullErr := pull.Run()
	if pullErr == nil {
		return nil
	}
	if s.image == DefaultSandboxImage {
		fmt.Fprintf(os.Stderr, "oc: pull failed (%v), building %s from the embedded Dockerfile\n", pullErr, s.image)
		return s.buildImage()
	}
	return fmt.Errorf("image %s not found locally and pull failed: %w (use -build to build it from oc's Dockerfile)", s.image, pullErr)
}

func (s *Sandbox) buildImage() error {
	fmt.Fprintf(os.Stderr, "oc: building %s\n", s.image)
	// A Dockerfile on stdin means no build context, which is all it needs.
	cmd := exec.Command("docker", "build", "-t", s.image, "-")
	cmd.Stdin = bytes.NewReader(sandboxDockerfile)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", s.image, err)
	}
	return nil
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
		"run", "--rm", flags,
		"--name", name,
		"--add-host", containerHost + ":host-gateway",
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
