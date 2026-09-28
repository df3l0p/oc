# The sandbox's egress proxy. Internal: not a selectable -image. oc streams
# this package's embedded source (proxy.Source, this file included) to
# `docker build -` as the build context, so the host needs Docker but no Go
# toolchain, and the binary matches the engine's architecture. The package
# directory is the module root here, which the internal-import rule allows:
# the importer is still under github.com/df3l0p/oc.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN go mod init github.com/df3l0p/oc/internal/proxy \
 && CGO_ENABLED=0 go build -trimpath -o /oc-proxy ./cmd/oc-proxy

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /oc-proxy /usr/local/bin/oc-proxy
ENTRYPOINT ["/usr/local/bin/oc-proxy"]
