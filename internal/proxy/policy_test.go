package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"allow":["host.docker.internal:8080","example.com:443"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if !pol.Allows("host.docker.internal:8080") || !pol.Allows("example.com:443") {
		t.Errorf("Allow = %v, missing an expected entry", pol.Allow)
	}
	if pol.Allows("evil.example:443") {
		t.Error("an unlisted host must not be allowed")
	}
}

func TestLoadPolicyMissingFile(t *testing.T) {
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("expected an error for a missing policy file")
	}
}

func TestLoadPolicyMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(path); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
