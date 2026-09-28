package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// errNotPublic is returned when a destination allowed only by the policy's *
// resolves to an address that isn't public.
var errNotPublic = errors.New("not a public address")

// cgnat is 100.64.0.0/10 (RFC 6598): carrier-grade NAT, and the range
// Tailscale and some VPNs hand out, so never "the internet".
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isPublic reports whether ip is a public unicast address: not loopback,
// link-local, multicast, unspecified, private (RFC 1918 / unique local), or
// CGNAT. It's what a policy's * lets through, keeping the host machine and
// the LAN out of reach even by IP.
func isPublic(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && cgnat.Contains(ip4) {
		return false
	}
	return true
}

// directDialer reaches destinations the policy lists explicitly, whatever
// their address (llama-server is on the host).
var directDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

// publicDialer reaches destinations allowed only by *. It checks the address
// it's actually connecting to, after DNS resolution, so a name that resolves
// to a private address can't slip through.
var publicDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if !isPublic(net.ParseIP(host)) {
			return fmt.Errorf("%s: %w", host, errNotPublic)
		}
		return nil
	},
}

// publicTransport is forwardTransport dialing through publicDialer.
var publicTransport = func() *http.Transport {
	t := forwardTransport.Clone()
	t.DialContext = publicDialer.DialContext
	return t
}()

// dialerFor picks how to reach hostport: directly if the policy lists it,
// otherwise public addresses only.
func (s *Server) dialerFor(hostport string) *net.Dialer {
	if s.Policy.Explicit(hostport) {
		return directDialer
	}
	return publicDialer
}

// transportFor is dialerFor for the forward path.
func (s *Server) transportFor(hostport string) *http.Transport {
	if s.Policy.Explicit(hostport) {
		return forwardTransport
	}
	return publicTransport
}

// dialFailed answers a request whose upstream couldn't be reached: 403 when
// the policy's public-only rule refused it, 502 otherwise.
func dialFailed(w http.ResponseWriter, target string, err error) {
	if errors.Is(err, errNotPublic) {
		denied(w, target+" (not a public address)")
		return
	}
	http.Error(w, "oc-proxy: dial upstream: "+err.Error(), http.StatusBadGateway)
}
