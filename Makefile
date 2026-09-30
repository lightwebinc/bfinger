# GOWORK=off everywhere on purpose, so a local go.work never masks go.mod:
# verify what ships, not what a workspace happens to have on disk.

.PHONY: verify build test fmt-check vet deps-check licences licences-update host host-test host-docker dist clean docker-build docker-smoke

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/lightwebinc/bfinger

verify: fmt-check vet deps-check licences build test

build:
	GOWORK=off go build ./...

test:
	GOWORK=off go test -race ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt:"; gofmt -l .; exit 1; }

vet:
	GOWORK=off go vet ./...

# The direct dependencies, asserted rather than intended. A dependency arriving
# by accident is how a small tool quietly acquires an engine, and it is
# invisible in a diff that only adds an import. There are two: go-sdk, and
# bcommon, the library of generic building blocks bfinger is made from. The
# checks, each against the build this module resolves:
#  - go.mod and go.sum are tidy, because every check below reads their
#    direct and indirect markers, and no module is replaced;
#  - the direct set is exactly those two;
#  - bcommon's own direct requirements are exactly go-sdk. They are its edges
#    in `go mod graph` at the version selected, less what its go.mod marks
#    indirect; a go.mod that cannot be read leaves every edge in, and fails;
#  - every module the library's packages import, of those bfinger's packages
#    and tests link, is go-sdk. The previous check trusts the library's
#    indirect markers, so a tag whose go.mod labels a module it imports as
#    indirect passes it; this one reads the imports, so a dependency cannot
#    arrive through the library instead. No linked package at all fails, as
#    an empty answer here is more likely a broken query than a clean library;
#  - go-sdk resolves to exactly v1.5.2, with no replacement. Minimal version
#    selection takes the higher of two pins, so a library asking for a later
#    go-sdk would change what bfinger ships without a line changing here.
DEPS_DIRECT := github.com/bsv-blockchain/go-sdk github.com/lightwebinc/bcommon
DEPS_LIB := github.com/lightwebinc/bcommon
DEPS_LIB_DIRECT := github.com/bsv-blockchain/go-sdk
DEPS_SDK := github.com/bsv-blockchain/go-sdk v1.5.2

deps-check:
	@set -eu; \
	if ! GOWORK=off go mod tidy -diff >/dev/null; then \
		echo "deps-check: go.mod or go.sum is not tidy, so its direct and indirect markers cannot be trusted; run go mod tidy"; \
		exit 1; \
	fi; \
	replaced=$$(GOWORK=off go list -m -f '{{if .Replace}}{{.Path}}{{end}}' all); \
	if [ -n "$$replaced" ]; then \
		echo "deps-check: no module may be replaced, found:"; echo "$$replaced" | sed 's/^/  /'; \
		exit 1; \
	fi; \
	direct=$$(GOWORK=off go list -m -f '{{if not (or .Main .Indirect)}}{{.Path}}{{end}}' all); \
	want=$$(printf '%s\n' $(DEPS_DIRECT) | sort); \
	got=$$(printf '%s\n' "$$direct" | sed '/^$$/d' | sort); \
	if [ "$$got" != "$$want" ]; then \
		echo "deps-check: the direct dependencies must be exactly:"; echo "$$want" | sed 's/^/  /'; \
		echo "found:"; echo "$$got" | sed 's/^/  /'; \
		exit 1; \
	fi; \
	v=$$(GOWORK=off go list -m -f '{{.Version}}' $(DEPS_LIB)); \
	gomod=$$(GOWORK=off go list -m -f '{{.GoMod}}' $(DEPS_LIB)); \
	indirect=$$( [ -n "$$gomod" ] && GOWORK=off go mod edit -print "$$gomod" | awk '/\/\/ indirect/ { print ($$1 == "require" ? $$2 : $$1) }' || true); \
	graph=$$(GOWORK=off go mod graph); \
	want=$$(printf '%s\n' $(DEPS_LIB_DIRECT) | sort); \
	got=$$(printf '%s\n' "$$graph" | awk -v n="$(DEPS_LIB)@$$v" '$$1 == n { sub(/@.*/, "", $$2); print $$2 }' \
		| grep -vxE 'go|toolchain' | grep -vxF "$$indirect" | sort -u || true); \
	if [ "$$got" != "$$want" ]; then \
		echo "deps-check: $(DEPS_LIB)@$$v must directly require exactly:"; echo "$$want" | sed 's/^/  /'; \
		echo "found:"; echo "$$got" | sed 's/^/  /'; \
		exit 1; \
	fi; \
	imports=$$(GOWORK=off go list -deps -test -f '{{with .Module}}{{if eq .Path "$(DEPS_LIB)"}}{{range $$.Imports}}{{printf "%s\n" .}}{{end}}{{end}}{{end}}' ./...); \
	imports=$$(printf '%s\n' "$$imports" | sed -e 's/ \[.*//' -e '/^$$/d' | sort -u); \
	if [ -z "$$imports" ]; then \
		echo "deps-check: no package of $(DEPS_LIB) is linked, so there are no imports to check"; \
		exit 1; \
	fi; \
	mods=$$(GOWORK=off go list -f '{{with .Module}}{{.Path}}{{end}}' $$imports); \
	got=$$(printf '%s\n' "$$mods" | sed '/^$$/d' | grep -vxF "$(DEPS_LIB)" | sort -u || true); \
	extra=$$(printf '%s\n' "$$got" | sed '/^$$/d' | grep -vxF "$$want" || true); \
	if [ -n "$$extra" ]; then \
		echo "deps-check: $(DEPS_LIB)@$$v's packages may import modules only from:"; echo "$$want" | sed 's/^/  /'; \
		echo "found:"; echo "$$got" | sed 's/^/  /'; \
		exit 1; \
	fi; \
	sdk=$$(GOWORK=off go list -m $(firstword $(DEPS_SDK))); \
	if [ "$$sdk" != "$(DEPS_SDK)" ]; then \
		echo "deps-check: go-sdk must resolve to exactly $(lastword $(DEPS_SDK)), found: $$sdk"; \
		exit 1; \
	fi

# The overlay host modules are a separate npm package with its own
# toolchain, so they are not part of `verify`: a machine that can build the
# command need not have Node on it. CI runs them in their own job, and this
# target is how to run them here. It leaves the deployable artefact,
# host/bundle/index.js; the bundle step fails, and removes the file, if
# anything but host/src/ and the bcommon package went into it.
host:
	cd host && npm ci && npm run check && npm run build && npm run bundle

host-test: host
	cd host && npm test

# The same, inside the Node 24 image, for a machine whose own Node is another
# major. It runs as the calling user, so host/node_modules, host/dist and
# host/bundle stay owned by whoever ran it, and it shares that user's npm
# cache, so a second run needs no registry. The bundle is left where `host`
# leaves it.
NODE_IMAGE     ?= node:24@sha256:64af3819f9275802414d7cdc38c27e9d82bd564dec4d4da87d008255d36c63b4
HOST_NPM_CACHE ?= $(HOME)/.npm

host-docker:
	mkdir -p $(HOST_NPM_CACHE)
	docker run --rm --user "$$(id -u):$$(id -g)" -e HOME=/tmp -e npm_config_cache=/npm \
		-v "$(HOST_NPM_CACHE)":/npm -v "$(CURDIR)":/src -w /src/host $(NODE_IMAGE) \
		sh -c 'npm ci --prefer-offline --no-audit --no-fund && npm run check && npm test'

# The release artefacts, built the way the release workflow builds them, so
# a tag can be rehearsed locally before it is pushed.
dist: clean
	@set -eu; v=$$(git describe --tags --always --dirty); \
	for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; d=bfinger_$${v}_$${os}_$${arch}; \
		mkdir -p dist/$$d; \
		GOWORK=off CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -buildvcs=false \
			-ldflags "-s -w -X main.version=$$v" -o dist/$$d/bfinger ./cmd/bfinger; \
		cp LICENSE NOTICE LICENSE-THIRD-PARTY README.md dist/$$d/; \
		tar -C dist -czf dist/$$d.tar.gz $$d; \
	done; \
	ls -l dist/*.tar.gz

clean:
	rm -rf dist
	rm -f bfinger

# Generated from what the binary actually links, so a dependency arriving by
# accident brings its licence obligation into the file rather than leaving it
# undischarged.
licences:
	python3 scripts/gen-third-party-licenses.py . --check

licences-update:
	python3 scripts/gen-third-party-licenses.py .

# The container image, built and tagged locally. Nothing here pushes: the
# image-publish workflow is the only thing that does.
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

# Runs the built image with no network at all, so everything it checks is
# something the image does on its own: the stamped version, help, and a
# first run in a fresh named volume mounted where the documentation says to
# mount one. The volume is the part most worth testing: a state directory
# the nonroot user cannot write refuses `init` and every pin. The pin uses
# the golden vector's published test key, checked against its fingerprint.
SMOKE_KEY := 0324653eac434488002cc06bbfb7f10fe18991e35f9fe4302dbea6d2353dc0ab1c
SMOKE_FP  := SHA256:p80oF5TbGEhPJA2b38CSIauJ63wV48p6tnwwY89tfjc

docker-smoke: docker-build
	@set -eu; img=$(IMAGE):$(VERSION); vol=bfinger-smoke-$$$$; \
	docker volume create $$vol >/dev/null; \
	trap 'docker volume rm -f $$vol >/dev/null' EXIT; \
	run() { docker run --rm --network none -v $$vol:/home/nonroot/.bfinger $$img "$$@"; }; \
	echo "== -version"; out=$$(run -version); echo "$$out"; \
	[ "$$out" = "bfinger $(VERSION)" ] || { echo "docker-smoke: want bfinger $(VERSION)"; exit 1; }; \
	echo "== -h"; run -h 2>&1 | head -1; \
	echo "== init"; run init; \
	echo "== doctor"; out=$$(run doctor); echo "$$out"; \
	echo "$$out" | grep -q '^headers     NOT CONFIGURED' || { echo "docker-smoke: doctor output unexpected"; exit 1; }; \
	echo "== keys trust"; run keys trust alice@example.com -key $(SMOKE_KEY) -fingerprint $(SMOKE_FP); \
	echo "== keys list"; run keys list | grep -F "fp=$(SMOKE_FP)"; \
	echo "== a lookup with no header source exits 2 before any request"; \
	rc=0; run alice@example.com || rc=$$?; [ "$$rc" = 2 ] || { echo "docker-smoke: want exit 2, got $$rc"; exit 1; }; \
	echo "docker-smoke: ok ($$img)"
