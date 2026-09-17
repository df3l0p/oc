// Package harness defines the interface oc uses to point a coding agent at
// a local model server and run it. Opencode is the only implementation
// today; a future agent can be added by implementing Harness without
// changing main's orchestration logic.
package harness

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
