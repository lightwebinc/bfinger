# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
#
# Multi-stage Dockerfile for bfinger. Produces a single static binary at
# /usr/local/bin/bfinger on a distroless nonroot base.
#
# The builder is pinned by digest and matches go.mod's Go version, so the
# stdlib scanning sees is the one that ships.
#
# No ENV defaults are baked in. The host and the facade are addresses of
# somebody's deployment and have no default anywhere; the header source
# defaults to the public WhatsOnChain service on the network, and every
# header it answers is checked for its proof of work. Pass settings as flags,
# BFINGER_* variables or a config file. See docs/configuration.md.
#
# The command keeps state (a pinned-key store, and for a publisher a wallet
# and a journal) under $BFINGER_HOME, ~/.bfinger by default,
# which is /home/nonroot/.bfinger here. Mount a volume there for anything but
# a one-shot read:
#
#   docker run --rm -v bfinger-state:/home/nonroot/.bfinger \
#     ghcr.io/lightwebinc/bfinger:<tag> \
#     -header-url https://headers.example.com alice@example.com
#
# The image carries that directory, empty, owned by nonroot and mode 0700, so
# a new named volume mounted there starts with the same owner and mode. A
# volume mounted on a path the image lacks is created root-owned, and every
# write the command makes to it is then refused. A bind mount keeps the host
# directory's owner instead: make it writable by uid 65532, or run with
# --user. See docs/configuration.md.

# The builder runs on the build machine's own platform and cross-compiles to
# the target's, so an arm64 image needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    mkdir -p /out /state/.bfinger; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/bfinger ./cmd/bfinger

FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
USER nonroot:nonroot
COPY --from=builder /out/ /usr/local/bin/
COPY --from=builder --chown=65532:65532 --chmod=0700 /state/.bfinger /home/nonroot/.bfinger

# The licenses travel with the image. The binary above statically contains
# go-sdk, whose license requires its own text to be included in any copy or
# substantial portion of the software, and static linking leaves that file
# behind. Source-tree-only compliance does not cover a published image.
COPY LICENSE NOTICE LICENSE-THIRD-PARTY /usr/share/doc/bfinger/

# No EXPOSE: bfinger listens on nothing reachable from outside the container
# (serve-wallet binds loopback only). It is a client of an overlay host, a
# header service and, for a publisher, a node and a submit facade.
ENTRYPOINT ["/usr/local/bin/bfinger"]
