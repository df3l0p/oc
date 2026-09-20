// Package llamaserver starts, health-checks, and stops a llama-server
// process, discovers the models it can serve, and tracks which oc processes
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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Options configures a llama-server invocation.
type Options struct {
	Host string
	Port int
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
	if runtime.GOOS == "darwin" {
		return nil, fmt.Errorf(`neither "llama" nor "llama-server" found on PATH (brew install llama.cpp provides both)`)
	}
	return nil, fmt.Errorf(`neither "llama" nor "llama-server" found on PATH`)
}

// Start launches the llama.cpp server in the background, using command as
// resolved by FindCommand (callers typically call FindCommand once up front
// to fail fast before doing other setup, then pass the result here rather
// than re-resolving it).
//
// No model is passed: without -m/-hf, llama-server runs in router mode. It
// lists every model in the local cache from /v1/models and loads one on
// demand when a request names it, so a single server (and any number of oc
// sessions sharing it) can serve any cached model, chosen per session. Its
// stdout/stderr are wired to the given writer
// (typically a log file, never the shared terminal — opencode's full-screen
// TUI runs on the same tty and raw log lines would corrupt its rendering).
// It does not wait for the server to become healthy; call WaitHealthy for
// that.
func Start(command []string, opts Options, output io.Writer) (*Process, error) {
	cmd := exec.Command(command[0], serverArgs(command, opts)...)
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

// serverArgs returns the arguments (after the executable) for a router-mode
// server: command's leading arguments plus host and port, deliberately with
// no model flag.
func serverArgs(command []string, opts Options) []string {
	args := append([]string{}, command[1:]...)
	return append(args, "--host", opts.Host, "--port", fmt.Sprintf("%d", opts.Port))
}

// Pid returns the server's process id.
func (p *Process) Pid() int { return p.cmd.Process.Pid }

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

// DiscoverModels queries /v1/models and returns every model id the server
// reports, in the order it lists them. A router-mode server reports every
// cached model (loaded or not); a server started with a single -hf model
// reports just that one. The list may be empty, e.g. a router with an empty
// cache.
func DiscoverModels(baseURL string) ([]string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/v1/models")
	if err != nil {
		return nil, fmt.Errorf("querying %s/v1/models: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s/v1/models returned %s: %s", baseURL, resp.Status, body)
	}

	var parsed modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parsing /v1/models response: %w", err)
	}
	ids := make([]string, len(parsed.Data))
	for i, m := range parsed.Data {
		ids[i] = m.ID
	}
	return ids, nil
}

// ResolveModel finds the served model id matching the requested name. An
// exact match wins; otherwise a request without a quant suffix (e.g.
// "unsloth/Qwen3.8-27B-GGUF") matches an id that adds one, preferring
// ":Q4_K_M" (llama.cpp's default quant) when several do.
func ResolveModel(ids []string, requested string) (string, bool) {
	for _, id := range ids {
		if id == requested {
			return id, true
		}
	}
	if strings.Contains(requested, ":") {
		return "", false
	}
	var found string
	for _, id := range ids {
		if !strings.HasPrefix(id, requested+":") {
			continue
		}
		if strings.EqualFold(id, requested+":Q4_K_M") {
			return id, true
		}
		if found == "" {
			found = id
		}
	}
	return found, found != ""
}

// Download fetches model into llama.cpp's cache with "llama download -hf",
// streaming progress to output. Only the unified "llama" CLI has this
// subcommand.
func Download(model string, output io.Writer) error {
	if _, err := exec.LookPath("llama"); err != nil {
		return fmt.Errorf("model %q is not in the llama.cpp cache and the \"llama\" CLI (needed to download it) is not on PATH", model)
	}
	cmd := exec.Command("llama", "download", "-hf", model)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("downloading %s: %w", model, err)
	}
	return nil
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
// still registered as using the llama-server on the given port. Registrations
// whose process no longer exists (an oc that was SIGKILLed or lost its
// terminal never unregistered) are ignored and removed; left in place they
// would make every later oc believe the server is still in use and never
// stop it.
func OtherSessionsActive(port int) bool {
	dir := registryDir(port)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	self := os.Getpid()
	active := false
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		if !processAlive(pid) {
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		active = true
	}
	return active
}

func processAlive(pid int) bool {
	// Signal 0 checks existence only; EPERM means it exists but isn't ours.
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// serverPIDPath is where the pid of an oc-started llama-server is recorded,
// so that whichever oc exits last can stop it, not only the one that started
// it. Without this, if the starter exits first (leaving the server up for
// the others), nobody ever stops it.
func serverPIDPath(port int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("oc-llama-server-%d.pid", port))
}

// RecordServer notes that pid is an oc-started llama-server on port.
func RecordServer(port, pid int) error {
	return os.WriteFile(serverPIDPath(port), []byte(strconv.Itoa(pid)), 0o600)
}

// StopRecordedServer stops the server recorded by RecordServer (SIGTERM to
// its process group, SIGKILL after gracePeriod) and clears the record. It is
// a no-op when nothing is recorded, e.g. for a server oc didn't start. The
// pid is checked to still belong to a llama process first, in case it exited
// earlier and the pid was reused.
func StopRecordedServer(port int) error {
	path := serverPIDPath(port)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	defer os.Remove(path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || !processAlive(pid) {
		return nil
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil || !strings.Contains(string(out), "llama") {
		return nil
	}

	pgid := -pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sending SIGTERM to llama-server: %w", err)
	}
	deadline := time.Now().Add(gracePeriod)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sending SIGKILL to llama-server: %w", err)
	}
	return nil
}
