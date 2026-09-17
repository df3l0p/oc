// Command oc starts a local llama-server, points opencode at it, and runs
// opencode in the current directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/df3l0p/oc/internal/llamaserver"
	"github.com/df3l0p/oc/internal/opencodeconfig"
)

const providerKey = "llama-cpp"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "oc:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	defaultConfigPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")

	model := flag.String("model", "ggml-org/Qwen2.5-Coder-7B-Instruct-GGUF:Q4_K_M", "model passed to llama-server's -hf flag")
	host := flag.String("host", "127.0.0.1", "llama-server host")
	port := flag.Int("port", 8080, "llama-server port")
	// TODO: to keep in mind, but I'd like to have a --sandbox flag for the harness to run on a container
	// mounts home cwd with container
	configPath := flag.String("opencode-config", defaultConfigPath, "path to opencode's config file")
	flag.Parse()

	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found on PATH: %w", err)
	}

	opts := llamaserver.Options{Model: *model, Host: *host, Port: *port}
	baseURL := opts.BaseURL()

	// Register as a user of this port's llama-server for the whole run, so
	// that if it's shared with another concurrent oc instance, whichever one
	// started it only stops it once every registered instance is gone.
	unregister, err := llamaserver.Register(*port)
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
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("oc-llama-server-%d.log", *port))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("creating llama-server log file: %w", err)
		}
		defer logFile.Close()

		fmt.Fprintf(os.Stderr, "oc: starting llama-server with model %s on %s (logs: %s)\n", *model, baseURL, logPath)
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

	modelID, err := llamaserver.DiscoverModelID(baseURL)
	if err != nil {
		return fmt.Errorf("discovering model id: %w", err)
	}

	provider := opencodeconfig.Provider{
		NPM:     "@ai-sdk/openai-compatible",
		Name:    "llama-server (local)",
		Options: map[string]interface{}{"baseURL": baseURL + "/v1"},
		Models: map[string]interface{}{
			modelID: map[string]interface{}{"name": modelID + " (local)"},
		},
	}
	if err := opencodeconfig.Merge(*configPath, providerKey, provider); err != nil {
		return fmt.Errorf("updating opencode config: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving current directory: %w", err)
	}

	opencodeCmd := exec.Command("opencode", ".")
	opencodeCmd.Dir = cwd
	// opencode only reads *configPath by default when it's left at its
	// default value (opencode's own default global config path). Setting
	// OPENCODE_CONFIG makes it read the file we just merged into even when
	// --opencode-config is overridden to something else.
	opencodeCmd.Env = append(os.Environ(), "OPENCODE_CONFIG="+*configPath)
	opencodeCmd.Stdin = os.Stdin
	opencodeCmd.Stdout = os.Stdout
	opencodeCmd.Stderr = os.Stderr

	// Ensures the parent (who launches the harness) is not killed and llama-server can be dealt properly and then leaves.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// block here until opencodeCmd is done
	runErr := opencodeCmd.Run()

	if startedByUs {
		if llamaserver.OtherSessionsActive(*port) {
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
