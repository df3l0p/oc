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
	// Run launches the harness in dir with modelID (one of the ids given to
	// Configure, served under providerKey) selected, wiring stdio to the
	// current process and blocking until it exits.
	Run(dir, providerKey, modelID string) error
}

// Preparer is implemented by harnesses that need setup before the model
// server starts (e.g. making sure a container image exists), so a failure
// there doesn't leave a server running for nothing.
type Preparer interface {
	Prepare() error
}

// BindHoster is implemented by harnesses that run the agent somewhere the
// host's loopback isn't reachable (a container), so the model server must
// listen on a different address. It returns the narrowest host address the
// agent can still reach; it must never widen to a wildcard.
type BindHoster interface {
	BindHost() (string, error)
}

// Options selects how a harness is run.
type Options struct {
	// Sandbox runs the agent in a Docker container instead of on the host.
	Sandbox bool
	// Image names the bundled sandbox image (see package images); empty means
	// images.Default.
	Image string
	// Build rebuilds the sandbox image (and the base image it layers on)
	// even if it already exists locally. A missing image is always built, never
	// pulled.
	Build bool
}

// registry maps a harness name, as accepted by oc's --harness flag, to a
// constructor for it. Each harness owns its own config path (e.g. opencode's
// default global config location) rather than taking one from oc's flags.
var registry = map[string]func(Options) (Harness, error){
	"opencode": newOpencode,
}

// New looks up name in the registry and constructs it, or returns an error
// listing the available names.
func New(name string, opts Options) (Harness, error) {
	ctor, ok := registry[name]
	if !ok {
		names := make([]string, 0, len(registry))
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown harness %q (available: %s)", name, strings.Join(names, ", "))
	}
	return ctor(opts)
}
