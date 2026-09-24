// Command oc starts a local llama-server, points opencode at it, and runs
// opencode in the current directory.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/df3l0p/oc/images"
	"github.com/df3l0p/oc/internal/harness"
	"github.com/df3l0p/oc/internal/llamaserver"
)

const providerKey = "llama-cpp"

// cliConfig holds oc's flags. Parsed into a struct (rather than package
// globals or ad-hoc locals in run()) so flag parsing stays in one place and
// run() takes a plain value.
type cliConfig struct {
	model   string
	host    string
	port    int
	harness string
	sandbox bool
	image   string
	build   bool
}

const defaultHost = "127.0.0.1"

// validate rejects flag combinations that only make sense with -sandbox.
func (c cliConfig) validate() error {
	if !c.sandbox && (c.image != "" || c.build) {
		return fmt.Errorf("-image and -build require -sandbox")
	}
	return nil
}

// listenHost is the address llama-server listens on, and that oc itself uses
// to reach it. An explicit -host is used as is (with a warning if it's a
// wildcard). With the default host, a harness that runs the agent in a
// container picks the narrowest address the container can reach, which on a
// native Linux engine isn't the host's loopback.
func listenHost(h harness.Harness, host string) (string, error) {
	if host != defaultHost {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			fmt.Fprintf(os.Stderr, "oc: warning: -host %s makes llama-server reachable from your whole network\n", host)
		}
		return host, nil
	}
	if b, ok := h.(harness.BindHoster); ok {
		return b.BindHost()
	}
	return host, nil
}

func parseFlags() (cliConfig, error) {
	var cfg cliConfig
	flag.StringVar(&cfg.model, "model", "unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M", "model to use in the harness (downloaded if not cached); every cached model is made available")
	flag.StringVar(&cfg.host, "host", defaultHost, "llama-server host (with -sandbox and the default, the narrowest address containers can reach: the loopback on Docker Desktop, the docker bridge gateway on Linux)")
	flag.IntVar(&cfg.port, "port", 8080, "llama-server port")
	flag.StringVar(&cfg.harness, "harness", "opencode", "coding agent harness to run (available: opencode)")
	flag.BoolVar(&cfg.sandbox, "sandbox", false, "run the harness in a Docker container that mounts the current directory")
	flag.StringVar(&cfg.image, "image", "", "bundled sandbox image to use (default "+images.Default+"; available: "+strings.Join(images.Names(), ", ")+"); requires -sandbox")
	flag.BoolVar(&cfg.build, "build", false, "rebuild the sandbox image from scratch, without docker's layer cache, even if it exists (it is built when missing, never pulled); requires -sandbox")
	flag.Parse()
	return cfg, cfg.validate()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "oc:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	h, err := harness.New(cfg.harness, harness.Options{Sandbox: cfg.sandbox, Image: cfg.image, Build: cfg.build})
	if err != nil {
		return err
	}
	if err := h.Available(); err != nil {
		return err
	}
	// Before starting any server, so a failed image pull/build leaves nothing
	// running.
	if p, ok := h.(harness.Preparer); ok {
		if err := p.Prepare(); err != nil {
			return err
		}
	}

	// oc talks to the server on the address it listens on: a server bound to a
	// specific interface doesn't answer on the loopback.
	host, err := listenHost(h, cfg.host)
	if err != nil {
		return err
	}
	opts := llamaserver.Options{Host: host, Port: cfg.port}
	baseURL := opts.BaseURL()
	if host != cfg.host {
		// A server already on the loopback would be reused by the host but
		// isn't reachable from the container, and starting another on the same
		// port would fail confusingly.
		if loopback := (llamaserver.Options{Host: cfg.host, Port: cfg.port}).BaseURL(); !llamaserver.IsHealthy(baseURL) && llamaserver.IsHealthy(loopback) {
			return fmt.Errorf("a llama-server at %s isn't reachable from the sandbox (it needs to listen on %s); stop it or use -port", loopback, host)
		}
	}

	// Register as a user of this port's llama-server for the whole run, so
	// that if it's shared with another concurrent oc instance, whichever one
	// started it only stops it once every registered instance is gone.
	unregister, err := llamaserver.Register(cfg.port)
	if err != nil {
		return err
	}
	defer unregister()

	var proc *llamaserver.Process

	if llamaserver.IsHealthy(baseURL) {
		fmt.Fprintf(os.Stderr, "oc: reusing existing llama-server at %s\n", baseURL)
	} else {
		command, err := llamaserver.FindCommand()
		if err != nil {
			return err
		}

		// llama-server's log lines must not go to os.Stderr: it shares this
		// terminal with opencode's full-screen TUI, and raw log output
		// interleaved with opencode's screen redraws corrupts the display.
		// Mode 0600 since the log may include request/model details tied to
		// the user's session.
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("oc-llama-server-%d.log", cfg.port))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("creating llama-server log file: %w", err)
		}
		defer logFile.Close()

		fmt.Fprintf(os.Stderr, "oc: starting llama-server on %s (logs: %s)\n", baseURL, logPath)
		proc, err = llamaserver.Start(command, opts, logFile)
		if err != nil {
			return err
		}
		if err := llamaserver.RecordServer(cfg.port, proc.Pid()); err != nil {
			_ = proc.Stop()
			return fmt.Errorf("recording llama-server pid: %w", err)
		}
		if err := llamaserver.WaitHealthy(baseURL, 2*time.Minute, proc); err != nil {
			_ = proc.Stop()
			_ = llamaserver.StopRecordedServer(cfg.port) // clears the record
			return fmt.Errorf("%w (see log: %s)", err, logPath)
		}
	}

	modelIDs, err := llamaserver.DiscoverModels(baseURL)
	if err != nil {
		return fmt.Errorf("discovering models: %w", err)
	}

	model, ok := llamaserver.ResolveModel(modelIDs, cfg.model)
	if !ok {
		fmt.Fprintf(os.Stderr, "oc: %s is not served by %s, trying to download it\n", cfg.model, baseURL)
		if err := llamaserver.Download(cfg.model, os.Stderr); err != nil {
			return err
		}
		modelIDs, err = llamaserver.DiscoverModels(baseURL)
		if err != nil {
			return fmt.Errorf("discovering models: %w", err)
		}
		model, ok = llamaserver.ResolveModel(modelIDs, cfg.model)
		if !ok {
			return fmt.Errorf("model %q is still not served by %s after download; if that llama-server was started with a fixed model (-hf/-m) or before the download, stop it (or use -port) so oc can start a fresh one", cfg.model, baseURL)
		}
	}

	if err := h.Configure(providerKey, baseURL, modelIDs); err != nil {
		return fmt.Errorf("updating opencode config: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving current directory: %w", err)
	}

	// opencode shares oc's foreground process group, so Ctrl+C delivers
	// SIGINT to both. Without this, Go's default disposition kills oc
	// immediately, skipping the llama-server cleanup below. Notifying (and
	// not reacting to) the signal here just stops oc from dying on its own;
	// opencode still receives and handles the signal directly.
	sigCh := make(chan os.Signal, 1)
	// SIGHUP (terminal closed) is included for the same reason, so cleanup
	// still runs once opencode exits.
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	runErr := h.Run(cwd, providerKey, model)

	// The last oc session out stops an oc-started server, even if a different
	// session started it. Servers oc didn't start have no record and are left
	// alone.
	if llamaserver.OtherSessionsActive(cfg.port) {
		fmt.Fprintln(os.Stderr, "oc: another oc session is still using llama-server, leaving it up")
	} else if proc != nil {
		fmt.Fprintln(os.Stderr, "oc: stopping llama-server")
		if err := proc.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "oc: failed to stop llama-server:", err)
		}
		_ = llamaserver.StopRecordedServer(cfg.port) // clears the record
	} else if err := llamaserver.StopRecordedServer(cfg.port); err != nil {
		fmt.Fprintln(os.Stderr, "oc: failed to stop llama-server:", err)
	}

	return runErr
}
