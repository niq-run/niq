PREFIX ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)

.PHONY: install build snapshot npm-publish

build:
	go build -ldflags "-X main.version=$(shell git describe --tags --always 2>/dev/null || echo dev)" -o ./bin/niq ./cmd/niq/

# Full local test of the release pipeline (no upload, no publish).
snapshot:
	goreleaser release --snapshot --clean
	./npm/publish-npm.sh "$(VERSION:v%=%)" ./dist --dry-run

# Publish npm packages from a goreleaser dist/ directory (run snapshot or
# goreleaser release first to build it).
#   Preview:  make npm-publish DRY=--dry-run
#   Publish:  make npm-publish            (or VERSION=x.y.z to override)
npm-publish:
	./npm/publish-npm.sh "$(VERSION:v%=%)" ./dist $(DRY)

install: build
	mkdir -p $(PREFIX)
	ln -sf "$(shell pwd)/bin/niq" $(PREFIX)/niq
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
