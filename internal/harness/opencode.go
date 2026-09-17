package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/df3l0p/oc/internal/opencodeconfig"
)

// Opencode drives opencode (github.com/sst/opencode) as a Harness. Its
// config path is fixed to opencode's own default global config location, so
// oc always merges into and points opencode at the same file it would use
// on its own.
type Opencode struct {
	configPath string
}

func newOpencode() (Harness, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	return Opencode{configPath: filepath.Join(home, ".config", "opencode", "opencode.jsonc")}, nil
}

func (o Opencode) Available() error {
	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found on PATH: %w", err)
	}
	return nil
}

func (o Opencode) Configure(providerKey, baseURL string, modelIDs []string) error {
	models := make(map[string]interface{}, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]interface{}{"name": id + " (local)"}
	}
	provider := opencodeconfig.Provider{
		NPM:     "@ai-sdk/openai-compatible",
		Name:    "llama-server (local)",
		Options: map[string]interface{}{"baseURL": baseURL + "/v1"},
		Models:  models,
	}
	return opencodeconfig.Merge(o.configPath, providerKey, provider)
}

func (o Opencode) Run(dir string) error {
	cmd := exec.Command("opencode", ".")
	cmd.Dir = dir
	// opencode only reads configPath by default when it's left at opencode's
	// own default global config path, which is exactly what configPath is
	// here. OPENCODE_CONFIG is set anyway so this keeps working even if a
	// future oc option makes configPath configurable again.
	cmd.Env = append(os.Environ(), "OPENCODE_CONFIG="+o.configPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
