PREFIX ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
WEBUI := app/webui/assets

.PHONY: install build webui snapshot npm-publish

# The SPA is go:embed'd from $(WEBUI)/dist, so it has to be rebuilt before the
# Go binary or the build ships whatever UI was last committed. node_modules is
# only installed when absent, so a repeat build is just tsc + vite.
webui:
	cd $(WEBUI) && { [ -d node_modules ] || npm ci; } && npm run build

build: webui
	go build -ldflags "-X main.version=$(shell git describe --tags --always 2>/dev/null || echo dev) -X main.buildTime=$(BUILDTIME)" -o ./bin/niq ./app/publish/cmd/

# Full local test of the release pipeline (no upload, no publish).
snapshot:
	goreleaser release --snapshot --clean --config app/publish/.goreleaser.yaml
	./app/publish/npm/publish-npm.sh "$(VERSION:v%=%)" ./dist --dry-run

# Publish npm packages from a goreleaser dist/ directory (run snapshot or
# goreleaser release first to build it).
#   Preview:  make npm-publish DRY=--dry-run
#   Publish:  make npm-publish            (or VERSION=x.y.z to override)
npm-publish:
	./app/publish/npm/publish-npm.sh "$(VERSION:v%=%)" ./dist $(DRY)

install: build
	mkdir -p $(PREFIX)
	ln -sf "$(shell pwd)/bin/niq" $(PREFIX)/niq
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
