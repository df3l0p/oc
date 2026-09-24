// Package images holds the sandbox images oc ships, one <name>.Dockerfile per
// image.
//
// base is oc's internal foundation: it installs the agent and runs as a
// non-root user. It is not offered to users. Every other image is a selectable
// layer on base (`oc -sandbox -image <name>`, default is Default): it declares
// `ARG OC_BASE`, its final stage is `FROM ${OC_BASE}` (oc passes the built base
// image as that build arg), and it ends as `USER oc`.
package images

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.Dockerfile
var files embed.FS

const suffix = ".Dockerfile"

const (
	// Base is the internal image every other image is layered on. It is never
	// selectable.
	Base = "base"
	// Default is the image used when none is chosen.
	Default = "default"
)

// Names lists the selectable image names, sorted; the base is not among them.
func Names() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err) // the embedded FS always has a root
	}
	var names []string
	for _, e := range entries {
		if n := strings.TrimSuffix(e.Name(), suffix); n != Base {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Read returns the Dockerfile of the named selectable image. Only exact names
// from Names resolve; anything else, including paths and the base, is an error
// listing what exists.
func Read(name string) ([]byte, error) {
	for _, n := range Names() {
		if n == name {
			return files.ReadFile(name + suffix)
		}
	}
	return nil, fmt.Errorf("unknown sandbox image %q (available: %s)", name, strings.Join(Names(), ", "))
}

// ReadBase returns the Dockerfile of the base image.
func ReadBase() ([]byte, error) {
	return files.ReadFile(Base + suffix)
}
