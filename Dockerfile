# syntax=docker/dockerfile:1.26@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
#
# Multi-stage Dockerfile for bfinger. Produces a single static binary at
# /usr/local/bin/bfinger on a distroless nonroot base.
#
# The builder is pinned by digest and is a LATER toolchain than go.mod's floor.
# The floor is held on purpose, while the stdlib actually shipped comes from
# here, so scanning sees what we ship rather than what we compile against.
#
# No ENV defaults are baked in. Three settings are addresses of somebody's
# deployment and deliberately have no default anywhere, the header source
# above all: a default there would send verification questions to a third
# party. Pass them as flags or in a config file. See docs/owner-flow.md.
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
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS builder
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

FROM gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
USER nonroot:nonroot
COPY --from=builder /out/ /usr/local/bin/
COPY --from=builder --chown=65532:65532 --chmod=0700 /state/.bfinger /home/nonroot/.bfinger

# The licences travel with the image. The binary above statically contains
# go-sdk, whose licence requires its own text to be included in any copy or
# substantial portion of the software, and static linking leaves that file
# behind. Source-tree-only compliance does not cover a published image.
COPY LICENSE NOTICE LICENSE-THIRD-PARTY /usr/share/doc/bfinger/

# No EXPOSE: bfinger listens on nothing reachable from outside the container
# (serve-wallet binds loopback only). It is a client of an overlay host, a
# header service and, for a publisher, a node and a submit facade.
ENTRYPOINT ["/usr/local/bin/bfinger"]
