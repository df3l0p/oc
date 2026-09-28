package proxy

import "embed"

// Source is this package's own source, the oc-proxy command under cmd/, and
// the Dockerfile at its root: the complete build context for the proxy image,
// so oc can build it with nothing but Docker. The image compiles it
// standalone, which is why this package must import only the standard
// library.
//
//go:embed *.go cmd Dockerfile
var Source embed.FS
