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

	"github.com/df3l0p/oc/internal/proxy"
)

func main() {
	addr := flag.String("addr", ":8888", "address to listen on")
	policyPath := flag.String("policy", "/etc/oc-proxy/policy.txt", "path to the allow-list policy file (one host:port per line)")
	flag.Parse()

	pol, err := proxy.LoadPolicy(*policyPath)
	if err != nil {
		log.Fatalf("oc-proxy: %v", err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("oc-proxy: %v", err)
	}
	// oc waits for this line before starting the sandbox, so print it only
	// once the port is actually bound.
	fmt.Printf("oc-proxy: listening on %s\n", ln.Addr())
	log.Fatal(http.Serve(ln, &proxy.Server{Policy: pol}))
}
