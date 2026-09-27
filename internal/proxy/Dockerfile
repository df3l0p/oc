# internal/proxy/Dockerfile
#
# The sandbox's egress proxy. Internal: not a selectable -image. oc lays the
# proxy's embedded source out under internal/proxy/ in the build context and
# builds it here, so the host needs Docker but no Go toolchain, and the
# binary matches the engine's architecture.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN go mod init github.com/df3l0p/oc \
 && CGO_ENABLED=0 go build -trimpath -o /oc-proxy ./internal/proxy/cmd/oc-proxy

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /oc-proxy /usr/local/bin/oc-proxy
ENTRYPOINT ["/usr/local/bin/oc-proxy"]
