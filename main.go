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
	configPath := flag.String("opencode-config", defaultConfigPath, "path to opencode's config file")
	flag.Parse()

	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found on PATH: %w", err)
	}

	opts := llamaserver.Options{Model: *model, Host: *host, Port: *port}
	baseURL := opts.BaseURL()

	var proc *llamaserver.Process
	startedByUs := false

	if llamaserver.IsHealthy(baseURL) {
		fmt.Fprintf(os.Stderr, "oc: reusing existing llama-server at %s\n", baseURL)
	} else {
		if _, err := exec.LookPath("llama-server"); err != nil {
			return fmt.Errorf("llama-server not found on PATH: %w", err)
		}

		// llama-server's log lines must not go to os.Stderr: it shares this
		// terminal with opencode's full-screen TUI, and raw log output
		// interleaved with opencode's screen redraws corrupts the display.
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("oc-llama-server-%d.log", *port))
		logFile, err := os.Create(logPath)
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
		if err := llamaserver.WaitHealthy(baseURL, 2*time.Minute); err != nil {
			_ = proc.Stop()
			return err
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
	opencodeCmd.Stdin = os.Stdin
	opencodeCmd.Stdout = os.Stdout
	opencodeCmd.Stderr = os.Stderr

	// opencode shares oc's foreground process group, so Ctrl+C delivers
	// SIGINT to both. Without this, Go's default disposition kills oc
	// immediately, skipping the llama-server cleanup below. Notifying (and
	// not reacting to) the signal here just stops oc from dying on its own;
	// opencode still receives and handles the signal directly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	runErr := opencodeCmd.Run()

	if startedByUs {
		if llamaserver.AnotherOpencodeRunning() {
			fmt.Fprintln(os.Stderr, "oc: another opencode is still running, leaving llama-server up")
		} else {
			fmt.Fprintln(os.Stderr, "oc: stopping llama-server")
			if err := proc.Stop(); err != nil {
				fmt.Fprintln(os.Stderr, "oc: failed to stop llama-server:", err)
			}
		}
	}

	return runErr
}
