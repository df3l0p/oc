// internal/harness/proxyimage_test.go
package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyContextLaysOutSourceForTheDockerfile(t *testing.T) {
	dir, err := proxyContext()
	if err != nil {
		t.Fatalf("proxyContext: %v", err)
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{
		"Dockerfile",
		"internal/proxy/proxy.go",
		"internal/proxy/cmd/oc-proxy/main.go",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("build context missing %s: %v", name, err)
		}
	}
}

// TestProxyContextCompilesStandalone reproduces the Dockerfile's build stage
// with the local Go toolchain instead of Docker: it's the fast, daemon-free
// check that proxyContext's layout is one the embedded package actually
// compiles in (in particular, that internal/proxy/source.go's own
// `//go:embed Dockerfile` resolves once Source is laid out under
// internal/proxy/ in a build context, not just in the real repo tree).
func TestProxyContextCompilesStandalone(t *testing.T) {
	dir, err := proxyContext()
	if err != nil {
		t.Fatalf("proxyContext: %v", err)
	}
	defer os.RemoveAll(dir)

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found on PATH: " + err.Error())
	}

	initCmd := exec.Command(goBin, "mod", "init", "github.com/df3l0p/oc")
	initCmd.Dir = dir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod init: %v\n%s", err, out)
	}

	out := filepath.Join(t.TempDir(), "oc-proxy")
	buildCmd := exec.Command(goBin, "build", "-o", out, "./internal/proxy/cmd/oc-proxy")
	buildCmd.Dir = dir
	buildCmd.Env = append(append([]string{}, os.Environ()...),
		"GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOWORK=off")
	if combined, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./internal/proxy/cmd/oc-proxy: %v\n%s", err, combined)
	}
}

func TestProxyImageTagIsStableAndNamed(t *testing.T) {
	a, err := proxyImageTag()
	if err != nil {
		t.Fatal(err)
	}
	b, err := proxyImageTag()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("tag not stable: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "oc-sandbox-proxy:") {
		t.Errorf("tag %q must be oc-sandbox-proxy:<hash>", a)
	}
}

func TestPrepareProxyImageBuildsWhenMissingAndCleansUpContext(t *testing.T) {
	logPath := fakeDocker(t, map[string]int{"image inspect": 1})
	tag, err := prepareProxyImage(false)
	if err != nil {
		t.Fatalf("prepareProxyImage: %v", err)
	}
	var buildDir string
	for _, c := range calls(t, logPath) {
		if strings.HasPrefix(c, "build -t "+tag+" ") {
			buildDir = strings.TrimPrefix(c, "build -t "+tag+" ")
		}
	}
	if buildDir == "" {
		t.Fatalf("expected `build -t %s <dir>`, got %q", tag, calls(t, logPath))
	}
	if _, err := os.Stat(buildDir); !os.IsNotExist(err) {
		t.Errorf("build context %s should be removed after the build, stat err = %v", buildDir, err)
	}
}

func TestPrepareProxyImageSkipsBuildWhenImageExists(t *testing.T) {
	logPath := fakeDocker(t, nil) // image inspect exits 0: the image exists
	if _, err := prepareProxyImage(false); err != nil {
		t.Fatalf("prepareProxyImage: %v", err)
	}
	for _, c := range calls(t, logPath) {
		if strings.HasPrefix(c, "build") {
			t.Errorf("must not build when the image exists, got %q", c)
		}
	}
}

func TestPrepareProxyImageBuildFlagRebuildsWithoutCache(t *testing.T) {
	logPath := fakeDocker(t, nil)
	tag, err := prepareProxyImage(true)
	if err != nil {
		t.Fatalf("prepareProxyImage: %v", err)
	}
	got := calls(t, logPath)
	if len(got) != 1 || !strings.HasPrefix(got[0], "build --no-cache -t "+tag+" ") {
		t.Errorf("-build must rebuild without inspecting or using the cache, got %q", got)
	}
}

func TestPrepareProxyImageBuildFailureIsAnError(t *testing.T) {
	fakeDocker(t, map[string]int{"image inspect": 1, "build": 1})
	if _, err := prepareProxyImage(false); err == nil || !strings.Contains(err.Error(), "building oc-sandbox-proxy:") {
		t.Fatalf("error = %v, want one naming the proxy image", err)
	}
}
