package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// ParsePolicy reads a Policy in its text form: one "host:port" per line;
// blank lines and everything after a '#' are ignored. A malformed line is an
// error — callers must never fall back to allow-all, or to a policy that
// differs from what was written.
func ParsePolicy(r io.Reader) (Policy, error) {
	var hostports []string
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		host, port, err := net.SplitHostPort(line)
		if err != nil || host == "" || strings.ContainsAny(line, " \t") {
			return Policy{}, fmt.Errorf("line %d: %q is not host:port", n, line)
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return Policy{}, fmt.Errorf("line %d: %q has an invalid port", n, line)
		}
		hostports = append(hostports, line)
	}
	if err := sc.Err(); err != nil {
		return Policy{}, err
	}
	return NewPolicy(hostports), nil
}

// LoadPolicy reads a Policy from a file in ParsePolicy's format. A missing
// or malformed file is an error.
func LoadPolicy(path string) (Policy, error) {
	f, err := os.Open(path)
	if err != nil {
		return Policy{}, fmt.Errorf("reading policy file %s: %w", path, err)
	}
	defer f.Close()
	pol, err := ParsePolicy(f)
	if err != nil {
		return Policy{}, fmt.Errorf("parsing policy file %s: %w", path, err)
	}
	return pol, nil
}
