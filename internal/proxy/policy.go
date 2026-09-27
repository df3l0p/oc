package proxy

import (
	"encoding/json"
	"fmt"
	"os"
)

// policyFile is the on-disk JSON shape read by LoadPolicy.
type policyFile struct {
	Allow []string `json:"allow"`
}

// LoadPolicy reads a Policy from a JSON file shaped
// {"allow": ["host:port", ...]}. A missing or malformed file is an error —
// callers must never fall back to allow-all.
func LoadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("reading policy file %s: %w", path, err)
	}
	var pf policyFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		return Policy{}, fmt.Errorf("parsing policy file %s: %w", path, err)
	}
	return NewPolicy(pf.Allow), nil
}
