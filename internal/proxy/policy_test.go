package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.txt")
	const policy = `# comments and blank lines are ignored

host.docker.internal:8080
  example.com:443   # so is a trailing comment, and surrounding space
`
	if err := os.WriteFile(path, []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if !pol.Allows("host.docker.internal:8080") || !pol.Allows("example.com:443") {
		t.Errorf("Allow = %v, missing an expected entry", pol.Allow)
	}
	if len(pol.Allow) != 2 {
		t.Errorf("Allow = %v, want exactly the two listed hosts", pol.Allow)
	}
	if pol.Allows("evil.example:443") {
		t.Error("an unlisted host must not be allowed")
	}
}

func TestParsePolicyEmptyAllowsNothing(t *testing.T) {
	pol, err := ParsePolicy(strings.NewReader("# nothing here\n\n"))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if len(pol.Allow) != 0 {
		t.Errorf("Allow = %v, want empty", pol.Allow)
	}
}

func TestLoadPolicyMissingFile(t *testing.T) {
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.txt")); err == nil {
		t.Fatal("expected an error for a missing policy file")
	}
}

// A malformed line is an error rather than silently skipped: a typo in an
// entry must not quietly leave the policy different from what was written.
func TestParsePolicyRejectsMalformedLines(t *testing.T) {
	for _, line := range []string{
		"example.com",           // no port
		"example.com:https",     // non-numeric port
		"example.com:0",         // port out of range
		"example.com:70000",     // port out of range
		":443",                  // no host
		"example.com:443 extra", // two fields
	} {
		_, err := ParsePolicy(strings.NewReader("github.com:443\n" + line + "\n"))
		if err == nil {
			t.Errorf("ParsePolicy accepted %q", line)
			continue
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Errorf("error for %q = %v, want it to name line 2", line, err)
		}
	}
}
