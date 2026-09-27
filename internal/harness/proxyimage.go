// internal/harness/proxyimage.go
package harness

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/df3l0p/oc/internal/proxy"
)

// proxyContext writes the proxy image's build context to a fresh temp dir:
// the Dockerfile at its root and proxy.Source under internal/proxy/, the
// layout the Dockerfile's `go build ./internal/proxy/cmd/oc-proxy` expects.
// The caller removes it.
func proxyContext() (string, error) {
	dir, err := os.MkdirTemp("", "oc-proxy-ctx-*")
	if err != nil {
		return "", fmt.Errorf("creating proxy build context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), proxy.Dockerfile, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("writing proxy Dockerfile: %w", err)
	}
	root := filepath.Join(dir, "internal", "proxy")
	err = fs.WalkDir(proxy.Source, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(root, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(proxy.Source, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("writing proxy source: %w", err)
	}
	return dir, nil
}

// proxyImageTag names the proxy image by a hash of its Dockerfile and every
// embedded source file (path and content), so any change to the proxy's code
// builds a new image and an unchanged one is reused.
func proxyImageTag() (string, error) {
	var in bytes.Buffer
	in.Write(proxy.Dockerfile)
	err := fs.WalkDir(proxy.Source, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(proxy.Source, p)
		if err != nil {
			return err
		}
		in.WriteByte(0)
		in.WriteString(p)
		in.WriteByte(0)
		in.Write(b)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hashing proxy source: %w", err)
	}
	return imageTag("proxy", in.Bytes(), ""), nil
}

// prepareProxyImage makes sure the proxy image exists locally, building it
// from the embedded source if it's missing, or always (without docker's layer
// cache) when build is set — the same rules Sandbox.Prepare applies to the
// sandbox images. Never pulled.
func prepareProxyImage(build bool) (string, error) {
	tag, err := proxyImageTag()
	if err != nil {
		return "", err
	}
	if !build && exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return tag, nil
	}

	dir, err := proxyContext()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	fmt.Fprintf(os.Stderr, "oc: building %s\n", tag)
	args := []string{"build", "-t", tag, dir}
	if build {
		args = []string{"build", "--no-cache", "-t", tag, dir}
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("building %s: %w", tag, err)
	}
	return tag, nil
}
