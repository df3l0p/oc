// internal/proxy/source_test.go
package proxy

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

func TestSourceEmbedsThePackageAndItsCommand(t *testing.T) {
	for _, name := range []string{"proxy.go", "policy.go", "source.go", "cmd/oc-proxy/main.go", "Dockerfile"} {
		if _, err := fs.Stat(Source, name); err != nil {
			t.Errorf("Source is missing %s: %v", name, err)
		}
	}
}

// The proxy image compiles Source on its own, with a go.mod that has no
// requirements, so a non-stdlib import would only break there, at image
// build time. Catch it here instead.
func TestSourceImportsOnlyTheStandardLibrary(t *testing.T) {
	err := fs.WalkDir(Source, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := fs.ReadFile(Source, path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parsing %s: %v", path, err)
			return nil
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			isStdlib := !strings.Contains(strings.SplitN(p, "/", 2)[0], ".")
			if !isStdlib && p != "github.com/df3l0p/oc/internal/proxy" {
				t.Errorf("%s imports %s; the proxy may import only the standard library and itself", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDockerfileBuildsTheEmbeddedCommand(t *testing.T) {
	if !strings.Contains(string(Dockerfile), "./internal/proxy/cmd/oc-proxy") {
		t.Errorf("Dockerfile doesn't build ./internal/proxy/cmd/oc-proxy:\n%s", Dockerfile)
	}
}
