package harness

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// untar extracts a proxyContext archive into dir.
func untar(t *testing.T, archive []byte, dir string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("reading build context: %v", err)
		}
		dst := filepath.Join(dir, filepath.FromSlash(h.Name))
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProxyContextIsTheEmbeddedSourceAsIs(t *testing.T) {
	archive, err := proxyContext()
	if err != nil {
		t.Fatalf("proxyContext: %v", err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading build context: %v", err)
		}
		names[h.Name] = true
	}
	for _, want := range []string{"Dockerfile", "proxy.go", "cmd/oc-proxy/main.go"} {
		if !names[want] {
			t.Errorf("build context missing %s (has %v)", want, names)
		}
	}
}

// TestProxyContextCompilesStandalone reproduces the Dockerfile's build stage
// with the local Go toolchain instead of Docker: the fast, daemon-free check
// that the embedded source compiles on its own from the context's root.
func TestProxyContextCompilesStandalone(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found on PATH: " + err.Error())
	}
	archive, err := proxyContext()
	if err != nil {
		t.Fatalf("proxyContext: %v", err)
	}
	dir := t.TempDir()
	untar(t, archive, dir)

	initCmd := exec.Command(goBin, "mod", "init", "github.com/df3l0p/oc/internal/proxy")
	initCmd.Dir = dir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod init: %v\n%s", err, out)
	}

	out := filepath.Join(t.TempDir(), "oc-proxy")
	buildCmd := exec.Command(goBin, "build", "-o", out, "./cmd/oc-proxy")
	buildCmd.Dir = dir
	buildCmd.Env = append(append([]string{}, os.Environ()...),
		"GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOWORK=off")
	if combined, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/oc-proxy: %v\n%s", err, combined)
	}
}

func TestProxyContextIsDeterministic(t *testing.T) {
	a, err := proxyContext()
	if err != nil {
		t.Fatal(err)
	}
	b, err := proxyContext()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("build context differs between calls, so the image tag would too")
	}
}

func TestPrepareProxyImageBuildsFromStdinWhenMissing(t *testing.T) {
	logPath := fakeDocker(t, map[string]int{"image inspect": 1})
	tag, err := prepareProxyImage(false)
	if err != nil {
		t.Fatalf("prepareProxyImage: %v", err)
	}
	if !strings.HasPrefix(tag, "oc-sandbox-proxy:") {
		t.Errorf("tag %q must be oc-sandbox-proxy:<hash>", tag)
	}
	got := calls(t, logPath)
	if want := "build -t " + tag + " -"; len(got) != 2 || got[1] != want {
		t.Errorf("docker calls = %q, want an inspect then %q", got, want)
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
	if want := "build --no-cache -t " + tag + " -"; len(got) != 1 || got[0] != want {
		t.Errorf("-build must rebuild without inspecting or using the cache: got %q, want [%q]", got, want)
	}
}

func TestPrepareProxyImageBuildFailureIsAnError(t *testing.T) {
	fakeDocker(t, map[string]int{"image inspect": 1, "build": 1})
	if _, err := prepareProxyImage(false); err == nil || !strings.Contains(err.Error(), "building oc-sandbox-proxy:") {
		t.Fatalf("error = %v, want one naming the proxy image", err)
	}
}
