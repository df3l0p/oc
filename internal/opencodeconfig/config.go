// Package opencodeconfig merges a provider block into opencode's JSONC
// config file without disturbing any of its other keys.
package opencodeconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Provider is the shape opencode expects for a custom OpenAI-compatible
// provider entry under the top-level "providers" key. Package is the
// provider's npm package, prefixed with "aisdk:" when it is an AI SDK
// package (as @ai-sdk/openai-compatible is).
type Provider struct {
	Package  string                 `json:"package"`
	Name     string                 `json:"name"`
	Settings map[string]interface{} `json:"settings"`
	Models   map[string]interface{} `json:"models"`
}

// Merge reads the JSONC config at path (treating a missing file as "{}"),
// sets provider[providerKey] to the given block, and writes the result back.
// All other keys in the file are left untouched. Comments in the original
// file are not preserved.
//
// The read-modify-write is guarded by an flock on a sibling ".lock" file, so
// two rnes processes racing on the same config don't silently drop one
// another's changes, and the write itself goes through a temp file + rename
// so a crash mid-write can't leave the config truncated.
func Merge(path, providerKey string, provider Provider) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", path, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		raw = nil
	}
	out, err := render(raw, providerKey, provider)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".rnes-config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// Render returns the config at path (a missing file counts as "{}") with
// provider[providerKey] set to the given block, as JSON, without writing
// anything. It's Merge's read-and-modify half, for callers that want a
// modified copy of the config rather than to change the original.
func Render(path, providerKey string, provider Provider) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := render(raw, providerKey, provider)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

func render(raw []byte, providerKey string, provider Provider) ([]byte, error) {
	config := map[string]interface{}{}
	if stripped := stripJSONComments(raw); len(bytes.TrimSpace(stripped)) > 0 {
		if err := json.Unmarshal(stripped, &config); err != nil {
			return nil, fmt.Errorf("parsing: %w", err)
		}
	}

	providers, _ := config["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	providerBlock, err := toMap(provider)
	if err != nil {
		return nil, fmt.Errorf("encoding provider block: %w", err)
	}
	providers[providerKey] = providerBlock
	config["providers"] = providers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return append(out, '\n'), nil
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

// DropProviderOption removes providers[providerKey].settings[option] from
// the JSON config in raw, reporting whether it was there. It also removes
// the legacy provider[providerKey].options[option] written before the v2
// config migration: the named option is what the sandbox must not see (an
// openrouter apiKey), and a config last written by the v1-era rnes would
// otherwise carry it into the container. Other keys are untouched.
func DropProviderOption(raw []byte, providerKey, option string) ([]byte, bool, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, false, fmt.Errorf("parsing: %w", err)
	}
	// Both locations are always tried: a config can carry the option in each.
	fromSettings := dropNested(config, "providers", providerKey, "settings", option)
	fromOptions := dropNested(config, "provider", providerKey, "options", option)
	if !fromSettings && !fromOptions {
		return raw, false, nil
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("encoding: %w", err)
	}
	return append(out, '\n'), true, nil
}

// dropNested deletes config[top][providerKey][group][option], reporting
// whether it was there. A missing level at any depth counts as absent.
func dropNested(config map[string]interface{}, top, providerKey, group, option string) bool {
	providers, _ := config[top].(map[string]interface{})
	block, _ := providers[providerKey].(map[string]interface{})
	values, _ := block[group].(map[string]interface{})
	if _, ok := values[option]; !ok {
		return false
	}
	delete(values, option)
	return true
}

// SetModel sets the top-level "model" default in the JSON config in raw, in
// opencode's "provider/model" format. The full TUI takes no model flag, so
// this is how a session starts on a chosen model without touching any
// config that outlives it.
func SetModel(raw []byte, model string) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	config["model"] = model
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return append(out, '\n'), nil
}
