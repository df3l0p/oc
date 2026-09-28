package harness

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/df3l0p/oc/internal/proxy"
)

// proxyContext returns the proxy image's build context as a tar archive of
// proxy.Source as-is: the package's source with its Dockerfile at the root.
// oc usually runs without a checkout, so the embedded copy is the only one it
// has; streaming it to `docker build -` means nothing is written to disk.
// The archive is deterministic (embedded files carry no timestamps), so it
// also serves as the image tag's hash input.
func proxyContext() ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.AddFS(proxy.Source); err != nil {
		return nil, fmt.Errorf("archiving proxy source: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("archiving proxy source: %w", err)
	}
	return buf.Bytes(), nil
}

// prepareProxyImage makes sure the proxy image exists locally, building it
// from the embedded source if it's missing, or always (without docker's layer
// cache) when build is set — the same rules Sandbox.Prepare applies to the
// sandbox images. Never pulled. The tag hashes the whole build context, so
// any change to the proxy's code builds a new image.
func prepareProxyImage(build bool) (string, error) {
	context, err := proxyContext()
	if err != nil {
		return "", err
	}
	tag := imageTag("proxy", context, "")
	if !build && exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return tag, nil
	}

	fmt.Fprintf(os.Stderr, "oc: building %s\n", tag)
	args := []string{"build", "-t", tag, "-"}
	if build {
		args = []string{"build", "--no-cache", "-t", tag, "-"}
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdin = bytes.NewReader(context)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("building %s: %w", tag, err)
	}
	return tag, nil
}
