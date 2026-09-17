// Package llamaserver starts, health-checks, and stops a llama-server
// process, discovers the model it's serving, and tracks which oc processes
// are currently using a given port so a shared server is only stopped once
// nobody needs it anymore.
package llamaserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Options configures a llama-server invocation.
type Options struct {
	Model string // passed to llama-server's -hf flag
	Host  string
	Port  int
}

// BaseURL returns the http://host:port form used for health checks and the
// OpenAI-compatible API.
func (o Options) BaseURL() string {
	return fmt.Sprintf("http://%s:%d", o.Host, o.Port)
}

// Process wraps a started llama-server so it can be stopped later.
type Process struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
}

// FindCommand locates the llama.cpp server on PATH, preferring the newer
// unified "llama serve" subcommand and falling back to the older standalone
// "llama-server" binary for installs that haven't picked up the newer CLI.
// The returned slice is the command and leading arguments to exec.
func FindCommand() ([]string, error) {
	if _, err := exec.LookPath("llama"); err == nil {
		return []string{"llama", "serve"}, nil
	}
	if _, err := exec.LookPath("llama-server"); err == nil {
		return []string{"llama-server"}, nil
	}
	return nil, fmt.Errorf(`neither "llama" nor "llama-server" found on PATH (on macOS, "brew install llama.cpp" provides both)`)
}

// Start launches the llama.cpp server in the background. Its stdout/stderr
// are wired to the given writer (typically a log file, never the shared
// terminal — opencode's full-screen TUI runs on the same tty and raw log
// lines would corrupt its rendering). It does not wait for the server to
// become healthy; call WaitHealthy for that.
func Start(opts Options, output io.Writer) (*Process, error) {
	command, err := FindCommand()
	if err != nil {
		return nil, err
	}
	args := append(command[1:], "-hf", opts.Model, "--host", opts.Host, "--port", fmt.Sprintf("%d", opts.Port))
	cmd := exec.Command(command[0], args...)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting llama-server: %w", err)
	}

	p := &Process{cmd: cmd, exited: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// gracePeriod is how long Stop waits for a clean shutdown (SIGTERM) before
// escalating to SIGKILL. llama-server can take several seconds to unmap a
// large model and release GPU buffers.
const gracePeriod = 10 * time.Second

// Stop terminates the llama-server process group and blocks until it has
// actually exited, escalating to SIGKILL if it doesn't shut down within
// gracePeriod. Without waiting here, oc would return control to the shell
// while llama-server is still mid-shutdown, making it look like Stop did
// nothing.
func (p *Process) Stop() error {
	if p == nil || p.cmd.Process == nil {
		return nil
	}
	pgid := -p.cmd.Process.Pid

	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sending SIGTERM to llama-server: %w", err)
	}

	select {
	case <-p.exited:
		return nil
	case <-time.After(gracePeriod):
	}

	if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sending SIGKILL to llama-server: %w", err)
	}
	<-p.exited
	return nil
}

// IsHealthy reports whether a server at baseURL responds successfully to
// GET /health.
func IsHealthy(baseURL string) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// WaitHealthy polls /health until it succeeds, proc exits, or timeout
// elapses. Watching proc.exited means a fast crash (bad model name, missing
// HF auth, no usable backend) is reported immediately instead of only after
// the full timeout.
func WaitHealthy(baseURL string, timeout time.Duration, proc *Process) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if IsHealthy(baseURL) {
			return nil
		}
		select {
		case <-proc.exited:
			return fmt.Errorf("llama-server exited before becoming healthy: %w", proc.waitErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("llama-server at %s did not become healthy within %s", baseURL, timeout)
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// DiscoverModelID queries /v1/models and returns the first model's id.
func DiscoverModelID(baseURL string) (string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/v1/models")
	if err != nil {
		return "", fmt.Errorf("querying %s/v1/models: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s/v1/models returned %s: %s", baseURL, resp.Status, body)
	}

	var parsed modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("parsing /v1/models response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return "", fmt.Errorf("no models reported by %s/v1/models", baseURL)
	}
	return parsed.Data[0].ID, nil
}

// registryDir is where oc processes register themselves as users of the
// llama-server on the given port, so a shared server started by one oc
// instance is only stopped once every oc instance using that port is gone.
// A directory of one file per PID needs no locking: create/remove are
// atomic, and listing tolerates concurrent add/remove.
func registryDir(port int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("oc-llama-server-%d.sessions", port))
}

// Register records this process as a user of the llama-server on the given
// port. Call the returned func when done, usually via defer.
func Register(port int) (func(), error) {
	dir := registryDir(port)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating session registry: %w", err)
	}
	self := filepath.Join(dir, fmt.Sprintf("%d", os.Getpid()))
	f, err := os.Create(self)
	if err != nil {
		return nil, fmt.Errorf("registering session: %w", err)
	}
	f.Close()
	return func() { os.Remove(self) }, nil
}

// OtherSessionsActive reports whether any oc process other than this one is
// still registered as using the llama-server on the given port.
func OtherSessionsActive(port int) bool {
	entries, err := os.ReadDir(registryDir(port))
	if err != nil {
		return false
	}
	self := fmt.Sprintf("%d", os.Getpid())
	for _, e := range entries {
		if e.Name() != self {
			return true
		}
	}
	return false
}
