package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/df3l0p/rnes/internal/opencodeconfig"
)

// Opencode drives opencode (github.com/anomalyco/opencode) as a Harness. Its
// config path is fixed to opencode's own default global config location, so
// rnes always merges into and points opencode at the same file it would use
// on its own.
type Opencode struct {
	configPath string
}

func newOpencode(opts Options) (Harness, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	configPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if opts.Sandbox {
		return newSandbox(opts, configPath), nil
	}
	return Opencode{configPath: configPath}, nil
}

// opencodeProvider builds the provider block that points opencode at a
// llama-server at baseURL, offering modelIDs. The package is an AI SDK
// package, so it carries opencode v2's "aisdk:" prefix.
func opencodeProvider(baseURL string, modelIDs []string) opencodeconfig.Provider {
	models := make(map[string]interface{}, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]interface{}{"name": id + " (local)"}
	}
	return opencodeconfig.Provider{
		Package:  "aisdk:@ai-sdk/openai-compatible",
		Name:     "llama-server (local)",
		Settings: map[string]interface{}{"baseURL": baseURL + "/v1"},
		Models:   models,
	}
}

func (o Opencode) Available() error {
	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found on PATH: %w", err)
	}
	return nil
}

func (o Opencode) Configure(providerKey, baseURL string, modelIDs []string) error {
	return opencodeconfig.Merge(o.configPath, providerKey, opencodeProvider(baseURL, modelIDs))
}

func (o Opencode) Run(dir, providerKey, modelID string) error {
	// v2's full TUI has no model flag (v1's -m is gone), and rewriting the
	// user's configured default model here would outlive the session. So on
	// the host the session starts on whatever model opencode would pick
	// anyway, and the llama-server models are one /models selection away;
	// providerKey and modelID are what the sandbox config writer uses (see
	// Sandbox.Run). The "." starts opencode in the working directory.
	cmd := exec.Command("opencode", ".")
	cmd.Dir = dir
	// opencode only reads configPath by default when it's left at opencode's
	// own default global config path, which is exactly what configPath is
	// here. OPENCODE_CONFIG is set anyway so this keeps working even if a
	// future rnes option makes configPath configurable again.
	cmd.Env = append(os.Environ(), "OPENCODE_CONFIG="+o.configPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
