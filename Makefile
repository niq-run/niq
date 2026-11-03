PREFIX ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
WEBUI := app/webui/assets
# Every step announces itself and npm logs each fetch, so a slow or stuck build
# says which stage and which host it is waiting on instead of looking frozen.
# Quiet npm down with: make build NPM_LOGLEVEL=notice
NPM_LOGLEVEL ?= http

.PHONY: install install_skip_ui build build_skip_ui webui snapshot npm-publish

# The SPA is go:embed'd from $(WEBUI)/dist, so it has to be rebuilt before the
# Go binary or the build ships whatever UI was last committed.
#
# Dependencies are keyed on npm's own completion marker, not on node_modules
# existing: npm creates the directory before it finishes, so an interrupted
# install used to leave a half-populated tree that a `-d` test trusted — the
# build then failed much later with "tsc: command not found". The marker is
# rewritten by npm after a successful install and is older than package-lock.json
# whenever the dependency set changed, so both cases reinstall.
#
# --include=dev keeps the build working on a machine that exports
# NODE_ENV=production, where npm would otherwise omit tsc and vite.
$(WEBUI)/node_modules/.package-lock.json: $(WEBUI)/package-lock.json
	@echo "webui: installing dependencies (npm ci)…"
	cd $(WEBUI) && npm ci --no-audit --no-fund --include=dev --loglevel=$(NPM_LOGLEVEL)

webui: $(WEBUI)/node_modules/.package-lock.json
	@echo "webui: building the bundle (tsc -b && vite build)…"
	cd $(WEBUI) && npm run build

GO_BUILD = go build -ldflags "-X main.version=$(shell git describe --tags --always 2>/dev/null || echo dev) -X main.buildTime=$(BUILDTIME)" -o ./bin/niq ./app/publish/cmd/

build: webui
	@echo "go: building ./bin/niq…"
	$(GO_BUILD)

# Go-only iteration: the same binary, without rebuilding the SPA first. The
# embed input is still $(WEBUI)/dist, so the committed bundle is what ships —
# this skips the rebuild, it does not change what gets embedded.
build_skip_ui:
	@echo "go: building ./bin/niq (SPA not rebuilt)…"
	$(GO_BUILD)

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

# The completion install comes after the link and is best-effort: it writes into
# a directory the shell already scans (zsh's fpath, fish's completions dir), so
# there is no dotfile edit and nothing to undo — a failure is not an install
# failure. NIQ_NO_COMPLETION=1 skips it.
define install_steps
	@echo "install: linking $(PREFIX)/niq → $(shell pwd)/bin/niq"
	mkdir -p $(PREFIX)
	ln -sf "$(shell pwd)/bin/niq" $(PREFIX)/niq
	-@if [ "$$NIQ_NO_COMPLETION" != "1" ]; then "$(PREFIX)/niq" completion install; fi
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
endef

install: build
	$(install_steps)

install_skip_ui: build_skip_ui
	$(install_steps)
