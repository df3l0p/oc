package harness

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points $TMPDIR at a directory of its own, removed afterwards: tests
// that run a sandbox session make the proxy's log file under it, and that file
// outlives the session by design.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oc-harness-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("TMPDIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
