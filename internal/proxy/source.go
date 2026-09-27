package proxy

import "embed"

// Source is this package's own source, including the oc-proxy command under
// cmd/, so oc can build the proxy image with nothing but Docker. The image
// compiles it standalone, which is why this package must import only the
// standard library. It also carries Dockerfile itself, since this package's
// own go:embed of Dockerfile below must resolve when Source is laid out
// under internal/proxy/ in a build context, not just in the real repo tree.
//
//go:embed *.go cmd Dockerfile
var Source embed.FS

// Dockerfile builds the proxy image from Source laid out under
// internal/proxy/ in the build context.
//
//go:embed Dockerfile
var Dockerfile []byte
