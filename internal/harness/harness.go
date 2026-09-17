// Package harness defines the interface oc uses to point a coding agent at
// a local model server and run it. Opencode is the only implementation
// today; a future agent (e.g. pi) can be added by implementing Harness and
// registering it in registry below, without changing main's orchestration
// logic.
package harness

import (
	"fmt"
	"sort"
	"strings"
)

// Harness configures a coding agent to use a local OpenAI-compatible model
// server, then runs it.
type Harness interface {
	// Available reports an error if the harness's executable can't be found.
	Available() error
	// Configure points the harness at baseURL under providerKey, offering
	// the given model ids.
	Configure(providerKey, baseURL string, modelIDs []string) error
	// Run launches the harness in dir, wiring stdio to the current process
	// and blocking until it exits.
	Run(dir string) error
}

// registry maps a harness name, as accepted by oc's --harness flag, to a
// constructor for it. configPath is the harness-specific config file to
// merge the local provider into (its meaning is up to each harness).
var registry = map[string]func(configPath string) Harness{
	"opencode": func(configPath string) Harness { return Opencode{ConfigPath: configPath} },
}

// New looks up name in the registry and constructs it, or returns an error
// listing the available names.
func New(name, configPath string) (Harness, error) {
	ctor, ok := registry[name]
	if !ok {
		names := make([]string, 0, len(registry))
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown harness %q (available: %s)", name, strings.Join(names, ", "))
	}
	return ctor(configPath), nil
}
