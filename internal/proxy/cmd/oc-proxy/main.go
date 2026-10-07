// Command oc-proxy is the sandbox's egress proxy. It is not meant to be run
// directly by a person: oc builds it into a small image and starts it as a
// container for the lifetime of one sandbox session.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/df3l0p/oc/internal/proxy"
)

func main() {
	addr := flag.String("addr", ":8888", "address to listen on")
	policyPath := flag.String("policy", "/etc/oc-proxy/policy.txt", "path to the allow-list policy file (one host:port per line)")
	intercept := flag.Bool("intercept", false, "terminate TLS for the hosts the policy lists explicitly, with a CA created for this run, and log each request")
	caOut := flag.String("ca-out", "/ca/ca.pem", "with -intercept: where to write the trust bundle (the system roots plus the session CA's certificate; the CA's key is never written)")
	systemRoots := flag.String("system-roots", "/etc/ssl/certs/ca-certificates.crt", "with -intercept: the system trust roots to include in the bundle")
	flag.Parse()

	pol, err := proxy.LoadPolicy(*policyPath)
	if err != nil {
		log.Fatalf("oc-proxy: %v", err)
	}
	srv := &proxy.Server{Policy: pol}
	if *intercept {
		ca, err := proxy.NewCA(pol.DNSHosts())
		if err != nil {
			log.Fatalf("oc-proxy: %v", err)
		}
		bundle, err := ca.Bundle(*systemRoots)
		if err != nil {
			log.Fatalf("oc-proxy: %v", err)
		}
		// World-readable: the sandbox reads it as another user, and it holds
		// only public certificates.
		if err := os.WriteFile(*caOut, bundle, 0o644); err != nil {
			log.Fatalf("oc-proxy: writing the trust bundle: %v", err)
		}
		fmt.Printf("oc-proxy: intercepting %v; trust bundle written to %s\n", pol.DNSHosts(), *caOut)
		srv.CA = ca
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("oc-proxy: %v", err)
	}
	// oc waits for this line before starting the sandbox, so print it only
	// once the port is actually bound (and, with -intercept, the bundle is
	// written, which it is by now).
	fmt.Printf("oc-proxy: listening on %s\n", ln.Addr())
	log.Fatal(http.Serve(ln, srv))
}
