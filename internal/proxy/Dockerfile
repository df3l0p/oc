# The sandbox's egress proxy. Internal: not a selectable -image. rnes streams
# this package's embedded source (proxy.Source, this file included) to
# `docker build -` as the build context, so the host needs Docker but no Go
# toolchain, and the binary matches the engine's architecture. The package
# directory is the module root here, which the internal-import rule allows:
# the importer is still under github.com/df3l0p/rnes.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN go mod init github.com/df3l0p/rnes/internal/proxy \
 && CGO_ENABLED=0 go build -trimpath -o /rnes-proxy ./cmd/rnes-proxy

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /rnes-proxy /usr/local/bin/rnes-proxy
ENTRYPOINT ["/usr/local/bin/rnes-proxy"]
