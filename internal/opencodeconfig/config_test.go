package opencodeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMergePreservesExistingKeysAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	original := `{
  // keep me
  "$schema": "https://opencode.ai/config.json"
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := Provider{
		Package:  "aisdk:@ai-sdk/openai-compatible",
		Name:     "llama-server (local)",
		Settings: map[string]interface{}{"baseURL": "http://127.0.0.1:8080/v1"},
		Models:   map[string]interface{}{"my-model": map[string]interface{}{"name": "my-model (local)"}},
	}

	if err := Merge(path, "llama-cpp", provider); err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	if got["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("expected $schema preserved, got %v", got["$schema"])
	}

	providers, ok := got["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected providers map, got %T", got["providers"])
	}
	block, ok := providers["llama-cpp"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected llama-cpp provider block, got %T", providers["llama-cpp"])
	}
	if block["package"] != "aisdk:@ai-sdk/openai-compatible" {
		t.Errorf("expected package field set, got %v", block["package"])
	}
	if block["settings"].(map[string]interface{})["baseURL"] != "http://127.0.0.1:8080/v1" {
		t.Errorf("expected settings.baseURL set, got %v", block["settings"])
	}
}

func TestMergeCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "opencode.jsonc")

	provider := Provider{Package: "aisdk:@ai-sdk/openai-compatible"}
	if err := Merge(path, "llama-cpp", provider); err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to be created: %v", err)
	}
}

func TestMergeIsIdempotentAndKeepsOtherProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	original := `{"provider": {"anthropic": {"options": {"apiKey": "x"}}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := Provider{Package: "aisdk:@ai-sdk/openai-compatible"}
	if err := Merge(path, "llama-cpp", provider); err != nil {
		t.Fatalf("first merge failed: %v", err)
	}
	if err := Merge(path, "llama-cpp", provider); err != nil {
		t.Fatalf("second merge failed: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	providers := got["providers"].(map[string]interface{})
	if _, ok := providers["llama-cpp"]; !ok {
		t.Error("expected llama-cpp provider to be present")
	}
	legacy := got["provider"].(map[string]interface{})
	if _, ok := legacy["anthropic"]; !ok {
		t.Error("expected the legacy anthropic provider block to be preserved")
	}
}

func TestMergeLeavesALegacyProviderBlockAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	// llama-cpp as an older rnes (or v1-era opencode) wrote it, plus an
	// unrelated v2 provider. The stale legacy entry stays where it is:
	// opencode resolves a provider/providers conflict in favor of the
	// native providers block, so the entry is inert, and rnes doesn't
	// rewrite config it doesn't manage.
	original := `{"provider": {"llama-cpp": {"npm": "@ai-sdk/openai-compatible", "options": {"baseURL": "http://old:1/v1"}}}, "providers": {"other": {"name": "x"}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := Provider{Package: "aisdk:@ai-sdk/openai-compatible"}
	if err := Merge(path, "llama-cpp", provider); err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	legacy := got["provider"].(map[string]interface{})
	if _, ok := legacy["llama-cpp"]; !ok {
		t.Error("expected the stale legacy llama-cpp block to be left alone")
	}
	providers := got["providers"].(map[string]interface{})
	if _, ok := providers["other"]; !ok {
		t.Error("expected the unrelated v2 provider to be preserved")
	}
	if _, ok := providers["llama-cpp"]; !ok {
		t.Error("expected llama-cpp under providers")
	}
}

func TestMergeConcurrentWritesDontDropEachOther(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	if err := os.WriteFile(path, []byte(`{"$schema": "https://opencode.ai/config.json"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("provider-%d", i)
			provider := Provider{Package: "aisdk:@ai-sdk/openai-compatible"}
			if err := Merge(path, key, provider); err != nil {
				t.Errorf("Merge %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	providers, ok := got["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected providers map, got %T", got["providers"])
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("provider-%d", i)
		if _, ok := providers[key]; !ok {
			t.Errorf("expected %s to survive concurrent merges, got keys %v", key, providers)
		}
	}
}

func TestStripJSONComments(t *testing.T) {
	in := `{
  // line comment
  "a": "value with // not a comment",
  /* block
     comment */
  "b": "value with /* not a comment */ still"
}`
	got := stripJSONComments([]byte(in))
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("stripped output not valid JSON: %v\n%s", err, got)
	}
	if m["a"] != "value with // not a comment" {
		t.Errorf("string content corrupted: %v", m["a"])
	}
	if m["b"] != "value with /* not a comment */ still" {
		t.Errorf("string content corrupted: %v", m["b"])
	}
}

func TestDropProviderOptionRemovesOnlyTheNamedOption(t *testing.T) {
	// The v2 shape (providers/openrouter/settings) and the pre-migration
	// shape (provider/legacy/options) both carry an apiKey; each call must
	// drop only the named provider's named option.
	in := []byte(`{"model":"m","providers":{"openrouter":{"settings":{"apiKey":"sk-or-REAL","x":1},"models":{"a":{}}}},"provider":{"legacy":{"options":{"apiKey":"sk-or-OLD","y":2}}}}`)
	out, dropped, err := DropProviderOption(in, "openrouter", "apiKey")
	if err != nil || !dropped {
		t.Fatalf("dropped = %v, err = %v", dropped, err)
	}
	if strings.Contains(string(out), "sk-or-REAL") || !strings.Contains(string(out), `"x": 1`) || !strings.Contains(string(out), `"models"`) {
		t.Errorf("out = %s", out)
	}
	out, dropped, err = DropProviderOption(out, "legacy", "apiKey")
	if err != nil || !dropped {
		t.Fatalf("legacy: dropped = %v, err = %v", dropped, err)
	}
	if strings.Contains(string(out), "sk-or-OLD") || !strings.Contains(string(out), `"y": 2`) || !strings.Contains(string(out), `"model"`) {
		t.Errorf("out = %s", out)
	}
	if _, dropped, _ := DropProviderOption(out, "openrouter", "apiKey"); dropped {
		t.Error("nothing left to drop, but dropped = true")
	}
}

func TestSetModelSetsTheTopLevelDefault(t *testing.T) {
	in := []byte(`{"theme":"dark","providers":{}}`)
	out, err := SetModel(in, "llama-cpp/m1")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got["model"] != "llama-cpp/m1" {
		t.Errorf("model = %v, want llama-cpp/m1", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", got["theme"])
	}
	// An existing default is replaced, not kept.
	out, err = SetModel(out, "llama-cpp/m2")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got["model"] != "llama-cpp/m2" {
		t.Errorf("model = %v, want llama-cpp/m2", got["model"])
	}
}
