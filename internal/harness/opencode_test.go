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

	providers := got["providers"].(map[string]interface{})
	block := providers["llama-cpp"].(map[string]interface{})
	models := block["models"].(map[string]interface{})

	for _, id := range []string{"model-a", "model-b"} {
		if _, ok := models[id]; !ok {
			t.Errorf("expected model %q to be configured, got %v", id, models)
		}
	}
	if block["package"] != "aisdk:@ai-sdk/openai-compatible" {
		t.Errorf("package = %v, want aisdk:@ai-sdk/openai-compatible", block["package"])
	}
	if got := block["settings"].(map[string]interface{})["baseURL"]; got != "http://127.0.0.1:8080/v1" {
		t.Errorf("baseURL = %v, want http://127.0.0.1:8080/v1", got)
	}
}

func TestNewOpencodeUsesHomeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	h, err := newOpencode(Options{})
	if err != nil {
		t.Fatalf("newOpencode: %v", err)
	}
	o := h.(Opencode)
	want := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if o.configPath != want {
		t.Errorf("configPath = %q, want %q", o.configPath, want)
	}
}

func TestOpencodeIsV2(t *testing.T) {
	for in, want := range map[string]bool{
		"opencode v2.0.32\n":            true,
		"2.0.32":                        true,
		"opencode 2.0.0":                true,
		"opencode v2.0.32 2026-10-17\n": true,
		"v2.0.32 (build 2026)":          true,
		"opencode version 2.0.32":       true,
		"opencode v2.0.32-beta.1":       true,
		"opencode v1.19.3":              false,
		"1.18.31":                       false,
		"opencode v12.0.0":              false,
		"opencode v1.19.3 2026-10-17":   false,
		"":                              false,
		"not a version":                 false,
	} {
		if err := opencodeIsV2(in); (err == nil) != want {
			t.Errorf("opencodeIsV2(%q) error = %v, want v2 accepted = %v", in, err, want)
		}
	}
}
