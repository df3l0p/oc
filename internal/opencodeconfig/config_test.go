package opencodeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		NPM:     "@ai-sdk/openai-compatible",
		Name:    "llama-server (local)",
		Options: map[string]interface{}{"baseURL": "http://127.0.0.1:8080/v1"},
		Models:  map[string]interface{}{"my-model": map[string]interface{}{"name": "my-model (local)"}},
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

	providers, ok := got["provider"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected provider map, got %T", got["provider"])
	}
	block, ok := providers["llama-cpp"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected llama-cpp provider block, got %T", providers["llama-cpp"])
	}
	if block["npm"] != "@ai-sdk/openai-compatible" {
		t.Errorf("expected npm field set, got %v", block["npm"])
	}
}

func TestMergeCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "opencode.jsonc")

	provider := Provider{NPM: "@ai-sdk/openai-compatible"}
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

	provider := Provider{NPM: "@ai-sdk/openai-compatible"}
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
	providers := got["provider"].(map[string]interface{})
	if _, ok := providers["anthropic"]; !ok {
		t.Error("expected existing anthropic provider to be preserved")
	}
	if _, ok := providers["llama-cpp"]; !ok {
		t.Error("expected llama-cpp provider to be present")
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
