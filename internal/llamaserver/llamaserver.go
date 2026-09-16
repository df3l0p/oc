// Package llamaserver starts, health-checks, and stops a llama-server
// process, and can discover the model it's serving.
package llamaserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
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
	cmd *exec.Cmd
}

// Start launches llama-server in the background. Its stdout/stderr are
// wired to the given writer (typically os.Stderr) so the user sees model
// load progress. It does not wait for the server to become healthy; call
// WaitHealthy for that.
func Start(opts Options, output io.Writer) (*Process, error) {
	cmd := exec.Command("llama-server",
		"-hf", opts.Model,
		"--host", opts.Host,
		"--port", fmt.Sprintf("%d", opts.Port),
	)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting llama-server: %w", err)
	}
	return &Process{cmd: cmd}, nil
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

	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()

	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sending SIGTERM to llama-server: %w", err)
	}

	select {
	case <-done:
		return nil
	case <-time.After(gracePeriod):
	}

	if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sending SIGKILL to llama-server: %w", err)
	}
	<-done
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

// WaitHealthy polls /health until it succeeds or timeout elapses.
func WaitHealthy(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if IsHealthy(baseURL) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
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

	var parsed modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("parsing /v1/models response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return "", fmt.Errorf("no models reported by %s/v1/models", baseURL)
	}
	return parsed.Data[0].ID, nil
}

// AnotherOpencodeRunning reports whether an opencode process is currently
// running anywhere on the machine.
func AnotherOpencodeRunning() bool {
	err := exec.Command("pgrep", "-x", "opencode").Run()
	return err == nil
}
