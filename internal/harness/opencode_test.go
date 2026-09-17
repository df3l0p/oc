package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpencodeConfigureWritesAllDiscoveredModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.jsonc")
	o := Opencode{configPath: path}

	if err := o.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"model-a", "model-b"}); err != nil {
		t.Fatalf("Configure: %v", err)
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
	block := providers["llama-cpp"].(map[string]interface{})
	models := block["models"].(map[string]interface{})

	for _, id := range []string{"model-a", "model-b"} {
		if _, ok := models[id]; !ok {
			t.Errorf("expected model %q to be configured, got %v", id, models)
		}
	}
	if got := block["options"].(map[string]interface{})["baseURL"]; got != "http://127.0.0.1:8080/v1" {
		t.Errorf("baseURL = %v, want http://127.0.0.1:8080/v1", got)
	}
}

func TestNewOpencodeUsesHomeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	h, err := newOpencode()
	if err != nil {
		t.Fatalf("newOpencode: %v", err)
	}
	o := h.(Opencode)
	want := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if o.configPath != want {
		t.Errorf("configPath = %q, want %q", o.configPath, want)
	}
}
