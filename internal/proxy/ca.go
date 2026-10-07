package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// caLifetime bounds both the CA and the certificates it issues. A CA lives as
// long as its proxy, which is one session, so this is only a ceiling: long
// enough that a session left running for days doesn't start failing
// verification, short enough that a leaked certificate expires.
const caLifetime = 30 * 24 * time.Hour

// CA is a per-session certificate authority for intercepting TLS. Its key
// exists only in memory, in the process that created it: it is never written
// out, so the only thing that leaves the proxy is the certificate.
//
// It is constrained to the hosts it was created for (an x509 name constraint),
// so a client that enforces constraints rejects anything it might issue for
// another host, and it refuses to issue for any host outside that list itself.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	hosts   map[string]bool

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewCA creates a CA that can vouch for exactly the given DNS host names.
// IP addresses and empty entries are ignored: an interception CA is never
// asked to vouch for an address. It is an error to end up with no host at all,
// because a CA without a name constraint could vouch for anything.
func NewCA(hosts []string) (*CA, error) {
	set := map[string]bool{}
	var names []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || net.ParseIP(h) != nil || set[h] {
			continue
		}
		set[h] = true
		names = append(names, h)
	}
	if len(names) == 0 {
		return nil, errors.New("proxy: a CA needs at least one DNS host to be constrained to")
	}
	sort.Strings(names)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "oc-proxy session CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(caLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,

		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         names,
		// Hosts only: the CA never vouches for an IP address.
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		hosts:   set,
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// Covers reports whether LeafFor can serve host.
func (c *CA) Covers(host string) bool {
	host = strings.ToLower(host)
	return net.ParseIP(host) == nil && c.hosts[host]
}

// CertPEM is the CA's certificate, PEM-encoded. It holds no secret.
func (c *CA) CertPEM() []byte { return c.certPEM }

// Bundle returns the PEM roots at systemRoots followed by this CA's
// certificate. Clients are pointed at it instead of the CA certificate alone
// because several trust variables (SSL_CERT_FILE, CURL_CA_BUNDLE, ...) replace
// the system roots rather than add to them, and the hosts that are not
// intercepted still present their real certificates.
func (c *CA) Bundle(systemRoots string) ([]byte, error) {
	roots, err := os.ReadFile(systemRoots)
	if err != nil {
		return nil, fmt.Errorf("reading system roots: %w", err)
	}
	if len(roots) > 0 && roots[len(roots)-1] != '\n' {
		roots = append(roots, '\n')
	}
	return append(roots, c.certPEM...), nil
}

// LeafFor returns the certificate presented to a client that asked for host,
// issuing it on first use. Only the exact hosts the CA was created for are
// served, even though its name constraint also covers their subdomains.
func (c *CA) LeafFor(host string) (*tls.Certificate, error) {
	host = strings.ToLower(host)
	if !c.Covers(host) {
		return nil, fmt.Errorf("proxy: no certificate for %q: not an intercepted host", host)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if leaf, ok := c.leaves[host]; ok {
		return leaf, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating key for %s: %w", host, err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     c.cert.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("issuing certificate for %s: %w", host, err)
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	c.leaves[host] = leaf
	return leaf, nil
}

// newSerial returns a random 128-bit certificate serial number.
func newSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}
	return serial, nil
}
