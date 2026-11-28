SHELL := /bin/bash
VERSION := $(shell cut -d' ' -f1 VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE := ghcr.io/orchard-suite/trellis
CONFIG ?= trellis.config.yaml
BRANCH ?=

.PHONY: check test build install docker run once land

# Everything that must pass before a commit: format, vet, every test, and
# trellis's own checkout judged by the packs it ships. It runs here before
# a commit lands, and in CI before every release.
check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: the files above need formatting" >&2; exit 1; }
	go vet ./...
	go test ./...
	go run ./cmd/trellis check -dir . -packs build,interfaces -fail-on high
	go run ./cmd/trellis test -packs-dir deploy/kubernetes/packs

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/trellis ./cmd/trellis

# The CLI into ~/.local/bin, from this checkout.
install: build
	mkdir -p $(HOME)/.local/bin
	install -m 0755 dist/trellis $(HOME)/.local/bin/trellis

docker:
	docker build --platform linux/amd64 -t $(IMAGE):$(VERSION) .

# A local board on the config's ports, for a config such as the one in
# README.md (sign_in: { mode: open } on a laptop).
run: build
	TUF_ROOT=$${TUF_ROOT:-/tmp/trellis-tuf} ./dist/trellis serve -config $(CONFIG)

# One pass of the board, printed, no server.
once: build
	TUF_ROOT=$${TUF_ROOT:-/tmp/trellis-tuf} ./dist/trellis once -config $(CONFIG)

# Lands main: release-damnit land runs the config's check (make check) on
# exactly what lands and signs its proof, which the release verifies. From a
# worktree: make land BRANCH=<branch>.
land:
	release-damnit land $(BRANCH)
