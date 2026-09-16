// Package opencodeconfig merges a provider block into opencode's JSONC
// config file without disturbing any of its other keys.
package opencodeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Provider is the shape opencode expects for a custom OpenAI-compatible
// provider entry under the top-level "provider" key.
type Provider struct {
	NPM     string                 `json:"npm"`
	Name    string                 `json:"name"`
	Options map[string]interface{} `json:"options"`
	Models  map[string]interface{} `json:"models"`
}

// Merge reads the JSONC config at path (treating a missing file as "{}"),
// sets provider[providerKey] to the given block, and writes the result back.
// All other keys in the file are left untouched. Comments in the original
// file are not preserved.
func Merge(path, providerKey string, provider Provider) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		raw = []byte("{}")
	}

	config := map[string]interface{}{}
	if stripped := stripJSONComments(raw); len(stripped) > 0 {
		if err := json.Unmarshal(stripped, &config); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	providers, _ := config["provider"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	providerBlock, err := toMap(provider)
	if err != nil {
		return fmt.Errorf("encoding provider block: %w", err)
	}
	providers[providerKey] = providerBlock
	config["provider"] = providers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func toMap(provider Provider) (map[string]interface{}, error) {
	raw, err := json.Marshal(provider)
	if err != nil {
		return nil, err
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// stripJSONComments removes // line comments and /* */ block comments from
// JSONC input, leaving string literals untouched, so the result can be fed
// to encoding/json.
func stripJSONComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(src); i++ {
		c := src[i]
		var next byte
		if i+1 < len(src) {
			next = src[i+1]
		}

		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
				out = append(out, c)
			}
		case inBlockComment:
			if c == '*' && next == '/' {
				inBlockComment = false
				i++
			}
		case inString:
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				out = append(out, next)
				i++
				continue
			}
			if c == '"' {
				inString = false
			}
		default:
			switch {
			case c == '"':
				inString = true
				out = append(out, c)
			case c == '/' && next == '/':
				inLineComment = true
				i++
			case c == '/' && next == '*':
				inBlockComment = true
				i++
			default:
				out = append(out, c)
			}
		}
	}
	return out
}
