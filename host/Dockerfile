# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
#
# finger-host: the reference overlay host (overlay-blueprints) with the finger
# module (tm_finger, ls_finger) baked in, so a host is one image and needs no
# file copied onto it. Build from the repository root:
#
#   docker build -f host/Dockerfile -t finger-host .
#
# BLUEPRINTS names the host image. It is a build argument, not a floating
# tag in a FROM line, so the release pins it and a rehearsal can point at a
# locally built one.

ARG BLUEPRINTS=ghcr.io/lightwebinc/overlay-blueprints:0.2.2

# The bundle is JavaScript, the same bytes on every platform, so it is built
# once on the build machine's own platform; the final stage only copies files
# onto the host image, so an arm64 image needs no emulation.
FROM --platform=$BUILDPLATFORM node:24@sha256:6449e19d8184d84062525b6a7524def03146c2cb04a7f08b076680d0e8e4240a AS bundle
WORKDIR /src/host
COPY host/package.json host/package-lock.json ./
COPY host/vendor ./vendor
RUN npm ci --no-audit --no-fund
COPY host/ ./
RUN npm run check && npm run build && npm run bundle

FROM ${BLUEPRINTS}
# Beside the host's own node_modules, so the bundle's one import (@bsv/sdk)
# resolves to the host's copy (see host/README.md).
COPY --from=bundle /src/host/bundle/index.js /app/modules/finger/index.js
COPY host/entrypoint.sh /usr/local/bin/finger-host
COPY LICENSE NOTICE LICENSE-THIRD-PARTY /usr/share/doc/bfinger/
ENV OVERLAY_TOPICS=tm_finger \
    OVERLAY_MODULES=/app/modules/finger/index.js
ENTRYPOINT ["/usr/local/bin/finger-host"]
