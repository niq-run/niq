PREFIX ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
WEBUI := app/webui/assets
# Every step announces itself and npm logs each fetch, so a slow or stuck build
# says which stage and which host it is waiting on instead of looking frozen.
# Quiet npm down with: make build NPM_LOGLEVEL=notice
NPM_LOGLEVEL ?= http

.PHONY: install build app webui snapshot npm-publish

# `make build` rebuilds the SPA first — it is go:embed-ed, so skipping that
# would ship whatever UI was last built. For Go-only iteration:
#
#	SKIP_WEBUI=1 make build:niq
#
# The build targets carry a colon in their names (build:niq, build:miq), which
# make cannot take in a prerequisite list — a colon there is the rule separator
# — so `build` recurses into them instead of listing them.
SKIP_WEBUI ?=

ifeq ($(SKIP_WEBUI),1)
WEBUI_PREREQ :=
else
WEBUI_PREREQ := webui
endif

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
BUILD_GOALS := build:niq build:miq
else
BUILD_GOALS := build:niq
endif

MAKEFILE_SELF := $(firstword $(MAKEFILE_LIST))

build:
	@$(MAKE) --no-print-directory -f $(MAKEFILE_SELF) $(BUILD_GOALS)

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

# The CLI, with the SPA rebuilt unless SKIP_WEBUI=1.
build\:niq: $(WEBUI_PREREQ)
	@echo "go: building ./bin/niq…"
	$(GO_BUILD)

# The menu bar app is a thin front end: it runs `niq status --json` and the
# per-project commands, so nothing here duplicates the CLI's logic. Built with
# the system Swift compiler into a .app bundle (LSUIElement: no Dock icon),
# plus ./bin/miq, the launcher that opens it.
APP := dist/niq.app

build\:miq:
	@echo "app: building $(APP) (swiftc)…"
	rm -rf "$(APP)"
	mkdir -p "$(APP)/Contents/MacOS"
	swiftc -O -swift-version 5 -framework AppKit app/menubar/main.swift \
		-o "$(APP)/Contents/MacOS/niq-menubar"
	cp app/menubar/Info.plist "$(APP)/Contents/Info.plist"
	printf 'APPL????' > "$(APP)/Contents/PkgInfo"
	@echo "app: building ./bin/miq (launcher)…"
	go build -o ./bin/miq ./app/menubar/miq
	@echo "app: built — run: ./bin/miq"

# `make app` is the older spelling of build:miq.
app:
	@$(MAKE) --no-print-directory -f $(MAKEFILE_SELF) build:miq

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
