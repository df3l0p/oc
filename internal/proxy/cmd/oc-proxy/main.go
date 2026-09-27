// Command oc-proxy is the sandbox's egress proxy. It is not meant to be run
// directly by a person: oc builds it into a small image and starts it as a
// container for the lifetime of one sandbox session.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/df3l0p/oc/internal/proxy"
)

func main() {
	addr := flag.String("addr", ":8888", "address to listen on")
	policyPath := flag.String("policy", "/etc/oc-proxy/policy.json", "path to the allow-list policy file")
	flag.Parse()

	pol, err := proxy.LoadPolicy(*policyPath)
	if err != nil {
		log.Fatalf("oc-proxy: %v", err)
	}
	srv := &proxy.Server{Policy: pol}
	fmt.Printf("oc-proxy: listening on %s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, srv))
}
