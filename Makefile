PREFIX ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
WEBUI := app/client/webui/assets
# Every step announces itself and npm logs each fetch, so a slow or stuck build
# says which stage and which host it is waiting on instead of looking frozen.
# Quiet npm down with: make build NPM_LOGLEVEL=notice
NPM_LOGLEVEL ?= http

.PHONY: install build niq miq webui miq-universal dist-miq app snapshot npm-publish

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
BUILD_PARTS := webui niq miq
else
BUILD_PARTS := webui niq
endif

# `make build` on its own is everything this machine can build. make has no
# notion of a subcommand — a word after `build` is another goal, not an argument
# — so `build` contributes prerequisites only when it stands alone, and each
# part is a target of its own:
#
#	make build              webui, then the CLI, then the app
#	make build webui        just the bundle
#	make build niq          just the CLI, embedding the bundle as it is
#	make build niq webui    the bundle, then the CLI
#	make build miq          the menu bar app
#
# `make niq` / `make miq` / `make webui` mean the same without the prefix.
ifneq (,$(strip $(filter-out build,$(MAKECMDGOALS))))
BUILD_PREREQ :=
WANT := $(filter-out build,$(MAKECMDGOALS))
else
BUILD_PREREQ := $(BUILD_PARTS)
WANT := $(BUILD_PARTS)
endif

build: $(BUILD_PREREQ)
	@# With a part named, this is empty and the recipe is what keeps make from
	@# printing "Nothing to be done for 'build'".
	@:

# Naming both must not depend on the order typed: the bundle is go:embed-ed, so
# the CLI has to be compiled after it.
ifneq (,$(and $(filter webui,$(WANT)),$(filter niq,$(WANT))))
niq: webui
endif

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

# The CLI. It embeds whatever bundle is in app/client/webui/assets/dist, so this does
# not rebuild the SPA — `make build webui` (or plain `make build`) is that step.
niq:
	@echo "go: building ./bin/niq…"
	$(GO_BUILD)

# The macOS distribution. Packaging lives with it (app/publish/mac/build.sh);
# these targets are the familiar entry points.
miq:
	@app/publish/mac/build.sh

miq-universal:
	@app/publish/mac/build.sh universal

dist-miq:
	@app/publish/mac/build.sh zip

# Full local test of the release pipeline (no upload, no publish).
snapshot:
	goreleaser release --snapshot --clean --config app/publish/cmd/.goreleaser.yaml
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
	-@if [ -x "$(shell pwd)/bin/miq" ]; then ln -sf "$(shell pwd)/bin/miq" $(PREFIX)/miq; echo "install: $(PREFIX)/miq → $(shell pwd)/bin/miq"; fi
	-@if [ "$$NIQ_NO_COMPLETION" != "1" ]; then "$(PREFIX)/niq" completion install; fi
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
endef

# install never builds: it links what is already there, and says what is missing
# instead of starting a build that was not asked for.
install:
	@if [ ! -x "$(shell pwd)/bin/niq" ]; then \
		echo "install: ./bin/niq is missing — run 'make build' first"; exit 1; fi
	$(install_steps)
