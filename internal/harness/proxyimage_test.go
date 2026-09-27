// internal/harness/proxyimage_test.go
package harness

import (
	"os"
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
