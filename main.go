// Command oc starts a local llama-server, points opencode at it, and runs
// opencode in the current directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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
}

func parseFlags() cliConfig {
	var cfg cliConfig
	flag.StringVar(&cfg.model, "model", "unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M", "model passed to llama-server's -hf flag")
	flag.StringVar(&cfg.host, "host", "127.0.0.1", "llama-server host")
	flag.IntVar(&cfg.port, "port", 8080, "llama-server port")
	flag.StringVar(&cfg.harness, "harness", "opencode", "coding agent harness to run (available: opencode)")
	// TODO: to keep in mind, but I'd like to have a --sandbox flag for the harness to run on a container
	// mounts home cwd with container
	flag.Parse()
	return cfg
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "oc:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := parseFlags()

	h, err := harness.New(cfg.harness)
	if err != nil {
		return err
	}
	if err := h.Available(); err != nil {
		return err
	}

	opts := llamaserver.Options{Model: cfg.model, Host: cfg.host, Port: cfg.port}
	baseURL := opts.BaseURL()

	// Register as a user of this port's llama-server for the whole run, so
	// that if it's shared with another concurrent oc instance, whichever one
	// started it only stops it once every registered instance is gone.
	unregister, err := llamaserver.Register(cfg.port)
	if err != nil {
		return err
	}
	defer unregister()

	var proc *llamaserver.Process
	startedByUs := false

	if llamaserver.IsHealthy(baseURL) {
		fmt.Fprintf(os.Stderr, "oc: reusing existing llama-server at %s\n", baseURL)
	} else {
		if _, err := llamaserver.FindCommand(); err != nil {
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

		fmt.Fprintf(os.Stderr, "oc: starting llama-server with model %s on %s (logs: %s)\n", cfg.model, baseURL, logPath)
		proc, err = llamaserver.Start(opts, logFile)
		if err != nil {
			return err
		}
		startedByUs = true
		if err := llamaserver.WaitHealthy(baseURL, 2*time.Minute, proc); err != nil {
			_ = proc.Stop()
			return fmt.Errorf("%w (see log: %s)", err, logPath)
		}
	}

	modelIDs, err := llamaserver.DiscoverModels(baseURL)
	if err != nil {
		return fmt.Errorf("discovering models: %w", err)
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
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	runErr := h.Run(cwd)

	if startedByUs {
		if llamaserver.OtherSessionsActive(cfg.port) {
			fmt.Fprintln(os.Stderr, "oc: another oc session is still using llama-server, leaving it up")
		} else {
			fmt.Fprintln(os.Stderr, "oc: stopping llama-server")
			if err := proc.Stop(); err != nil {
				fmt.Fprintln(os.Stderr, "oc: failed to stop llama-server:", err)
			}
		}
	}

	return runErr
}
