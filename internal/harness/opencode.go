package harness

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/df3l0p/oc/internal/opencodeconfig"
)

// Opencode drives opencode (github.com/sst/opencode) as a Harness.
type Opencode struct {
	// ConfigPath is the opencode config file to merge the local provider
	// into and to point opencode at via OPENCODE_CONFIG.
	ConfigPath string
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
	return opencodeconfig.Merge(o.ConfigPath, providerKey, provider)
}

func (o Opencode) Run(dir string) error {
	cmd := exec.Command("opencode", ".")
	cmd.Dir = dir
	// opencode only reads ConfigPath by default when it's left at opencode's
	// own default global config path. Setting OPENCODE_CONFIG makes it read
	// the file Configure just merged into even when ConfigPath points
	// somewhere else.
	cmd.Env = append(os.Environ(), "OPENCODE_CONFIG="+o.ConfigPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
