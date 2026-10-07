package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func parseCertPEM(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}
	return cert
}

func TestCALeafVerifiesAgainstCAForPermittedHost(t *testing.T) {
	ca, err := NewCA([]string{"github.com", "models.dev"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	leaf, err := ca.LeafFor("github.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("CA PEM not accepted by a cert pool")
	}
	parsed, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatalf("parsing leaf: %v", err)
	}
	if _, err := parsed.Verify(x509.VerifyOptions{Roots: roots, DNSName: "github.com"}); err != nil {
		t.Errorf("leaf does not verify for github.com: %v", err)
	}
}

func TestCAIsConstrainedToTheGivenHosts(t *testing.T) {
	ca, err := NewCA([]string{"github.com", "models.dev"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	cert := parseCertPEM(t, ca.CertPEM())
	if !cert.IsCA {
		t.Error("CA certificate is not marked as a CA")
	}
	if !reflect.DeepEqual(cert.PermittedDNSDomains, []string{"github.com", "models.dev"}) {
		t.Errorf("PermittedDNSDomains = %v, want [github.com models.dev]", cert.PermittedDNSDomains)
	}
	if !cert.PermittedDNSDomainsCritical {
		t.Error("the name constraint must be critical so clients that don't understand it reject the CA")
	}
}

func TestCALeafRefusedOutsideTheHostList(t *testing.T) {
	ca, err := NewCA([]string{"github.com"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	// example.com is outside the constraint; x.github.com is inside it (a DNS
	// constraint covers subdomains) but was never one of the listed hosts.
	for _, host := range []string{"example.com", "x.github.com"} {
		if _, err := ca.LeafFor(host); err == nil {
			t.Errorf("LeafFor(%q) succeeded, want an error", host)
		}
	}
}

func TestCALeafHostMatchIsCaseInsensitive(t *testing.T) {
	ca, err := NewCA([]string{"GitHub.com"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if _, err := ca.LeafFor("github.COM"); err != nil {
		t.Errorf("LeafFor: %v", err)
	}
}

func TestCARefusesIPAddresses(t *testing.T) {
	ca, err := NewCA([]string{"github.com", "10.0.0.1"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	cert := parseCertPEM(t, ca.CertPEM())
	if len(cert.PermittedDNSDomains) != 1 {
		t.Errorf("PermittedDNSDomains = %v, want only github.com (IP literals are never intercepted)", cert.PermittedDNSDomains)
	}
	if _, err := ca.LeafFor("10.0.0.1"); err == nil {
		t.Error("LeafFor(IP) succeeded, want an error")
	}
	if len(cert.ExcludedIPRanges) != 2 {
		t.Errorf("ExcludedIPRanges = %v, want all IPv4 and IPv6 excluded so the CA can't vouch for any address", cert.ExcludedIPRanges)
	}
}

func TestNewCARequiresAtLeastOneDNSHost(t *testing.T) {
	// Without a DNS host there is nothing to constrain the CA to, and a CA
	// without a name constraint could vouch for any host.
	for _, hosts := range [][]string{nil, {}, {"10.0.0.1"}, {""}} {
		if _, err := NewCA(hosts); err == nil {
			t.Errorf("NewCA(%v) succeeded, want an error", hosts)
		}
	}
}

func TestCALeafIsCached(t *testing.T) {
	ca, err := NewCA([]string{"github.com"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	a, err := ca.LeafFor("github.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	b, err := ca.LeafFor("github.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	if a != b {
		t.Error("a second LeafFor for the same host minted a new certificate")
	}
}

func TestCAAndItsLeavesAreValidFor30Days(t *testing.T) {
	ca, err := NewCA([]string{"github.com"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	leaf, err := ca.LeafFor("github.com")
	if err != nil {
		t.Fatalf("LeafFor: %v", err)
	}
	parsedLeaf, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatalf("parsing leaf: %v", err)
	}
	want := time.Now().Add(30 * 24 * time.Hour)
	for name, notAfter := range map[string]time.Time{
		"CA":   parseCertPEM(t, ca.CertPEM()).NotAfter,
		"leaf": parsedLeaf.NotAfter,
	} {
		if d := notAfter.Sub(want); d < -time.Minute || d > time.Minute {
			t.Errorf("%s NotAfter = %s, want about %s (30 days)", name, notAfter, want)
		}
	}
}

func TestEachCAIsDistinct(t *testing.T) {
	a, _ := NewCA([]string{"github.com"})
	b, _ := NewCA([]string{"github.com"})
	if string(a.CertPEM()) == string(b.CertPEM()) {
		t.Error("two CAs share a certificate; each session needs its own key")
	}
}

func TestBundleHoldsSystemRootsAndTheCA(t *testing.T) {
	ca, err := NewCA([]string{"github.com"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	other, _ := NewCA([]string{"models.dev"}) // stands in for a system root
	roots := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(roots, other.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	bundle, err := ca.Bundle(roots)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle is not valid PEM")
	}
	if n := strings.Count(string(bundle), "BEGIN CERTIFICATE"); n != 2 {
		t.Errorf("bundle has %d certificates, want the system root and the session CA", n)
	}
}

func TestBundleFailsWithoutSystemRoots(t *testing.T) {
	ca, err := NewCA([]string{"github.com"})
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if _, err := ca.Bundle(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Error("Bundle succeeded without a system roots file; blind-tunnelled hosts would stop verifying")
	}
}

func TestPolicyDNSHosts(t *testing.T) {
	pol, err := ParsePolicy(strings.NewReader(`
github.com:443
Models.dev:443
github.com:8443
10.0.0.1:443
[::1]:443
host.docker.internal:8080
`))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	got := pol.DNSHosts()
	want := []string{"github.com", "host.docker.internal", "models.dev"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DNSHosts() = %v, want %v (sorted, lower-cased, deduplicated, no IP literals)", got, want)
	}
}
