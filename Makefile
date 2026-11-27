PREFIX ?= $(HOME)/.local/bin
APP_DIR ?= /Applications
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
WEBUI := app/client/webui/assets
# Every step announces itself and npm logs each fetch, so a slow or stuck build
# says which stage and which host it is waiting on instead of looking frozen.
# Quiet npm down with: make build NPM_LOGLEVEL=notice
NPM_LOGLEVEL ?= http

.PHONY: install build niq webui desktop desktop-sidecar dist-desktop app snapshot npm-publish e2e e2e-cli e2e-bus e2e-worker

# The Tauri CLI comes in two flavours: `cargo tauri` (cargo install tauri-cli) or
# a standalone `tauri` (npm i -g @tauri-apps/cli, prebuilt — much faster). Both
# are resolved past PATH, because a GUI-launched make may not have ~/.cargo/bin
# or the npm global bin on it.
CARGO ?= $(shell command -v cargo 2>/dev/null || echo $(HOME)/.cargo/bin/cargo)
TAURI ?= $(shell command -v tauri 2>/dev/null)

# The desktop shell needs Go + Rust + the Tauri CLI. `make build` includes it
# whenever that toolchain is present and quietly skips it otherwise, so a
# CLI-only machine still builds. `make desktop` always tries.
DESKTOP_TOOLCHAIN := $(shell { test -x "$(CARGO)" && { test -n "$(TAURI)" || "$(CARGO)" tauri --version >/dev/null 2>&1; }; } >/dev/null 2>&1 && echo yes)
BUILD_PARTS := webui niq $(if $(DESKTOP_TOOLCHAIN),desktop)

# `make build` on its own is everything this machine can build. make has no
# notion of a subcommand — a word after `build` is another goal, not an argument
# — so `build` contributes prerequisites only when it stands alone, and each
# part is a target of its own:
#
#	make build              webui, the CLI, and (with Rust) the desktop app
#	make build webui        just the bundle
#	make build niq          just the CLI, embedding the bundle as it is
#	make build niq webui    the bundle, then the CLI
#	make build desktop      just the installable app
#
# `make niq` / `make webui` / `make desktop` mean the same without the prefix.
# The native macOS app lives in the clients repository.
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

# The installable desktop shell: bundles the CLI as a Tauri sidecar and opens the
# gateway's own WebUI. The project lives in app/client/desktop; releasing it is
# app/publish/desktop.
DESKTOP_APP := app/client/desktop/src-tauri/target/release/bundle/macos/niq.app

desktop: niq
	@test -x "$(CARGO)" || { echo "desktop: cargo is required — install Rust (https://rustup.rs)"; exit 1; }
	@cd app/client/desktop && NIQ_BIN="$(CURDIR)/bin/niq" ./scripts/prepare-sidecar.sh
	@if [ -n "$(TAURI)" ]; then \
		echo "desktop: using $(TAURI)"; \
		cd app/client/desktop && PATH="$(dir $(CARGO)):$$PATH" "$(TAURI)" build; \
	elif "$(CARGO)" tauri --version >/dev/null 2>&1; then \
		echo "desktop: using cargo tauri"; \
		cd app/client/desktop && "$(CARGO)" tauri build; \
	else \
		echo "desktop: the Tauri CLI is required — npm i -g @tauri-apps/cli"; exit 1; \
	fi

# Stage the sidecar without building the bundle (fast check that the CLI is
# picked up).
desktop-sidecar: niq
	@cd app/client/desktop && NIQ_BIN="$(CURDIR)/bin/niq" ./scripts/prepare-sidecar.sh

# Release artifacts for the desktop channel (dmg + zipped app under dist/).
#   make dist-desktop                    host build
#   make dist-desktop UNIVERSAL=1        universal (lipo'd CLI + app)
dist-desktop:
	@./app/publish/desktop/release.sh $(if $(UNIVERSAL),--universal,)

# Note: the native macOS app is no longer part of this repository. It lives in
# the clients repository (github.com/niq-run/clients), together with the other
# third-party clients — this repo ships the CLI, the WebUI, the TUI and the
# desktop shell above.

# Full local test of the release pipeline (no upload, no publish).
# End-to-end suites: black-box tests that build the CLI and drive it the way a
# user does. They are behind the `e2e` build tag, so `go test ./...` stays fast;
# run them when you change the command surface or the process model.
e2e:
	go test -tags e2e ./e2e/suites/... -count=1 -timeout 600s

e2e-cli:
	go test -tags e2e ./e2e/suites/cli -count=1 -timeout 600s

e2e-bus:
	go test -tags e2e ./e2e/suites/bus -count=1 -timeout 600s

e2e-worker:
	go test -tags e2e ./e2e/suites/worker -count=1 -timeout 600s

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
	-@if [ "$$NIQ_NO_COMPLETION" != "1" ]; then "$(PREFIX)/niq" completion install; fi
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
endef

# The desktop app is copied (not linked): LaunchServices, Spotlight and the
# quarantine/upgrade paths all want a real bundle. APP_DIR is overridable for a
# no-sudo, per-user install: make install APP_DIR=$$HOME/Applications
define install_app_steps
	@if [ -d "$(DESKTOP_APP)" ]; then \
		mkdir -p "$(APP_DIR)" && \
		echo "install: copying $(DESKTOP_APP) → $(APP_DIR)/niq.app" && \
		rm -rf "$(APP_DIR)/niq.app" && \
		ditto "$(DESKTOP_APP)" "$(APP_DIR)/niq.app" && \
		echo "niq.app installed → $(APP_DIR)/niq.app" || { \
			echo "install: could not write $(APP_DIR) — retry with APP_DIR=$$HOME/Applications"; exit 1; }; \
	else \
		echo "install: niq.app not built — run 'make desktop' (or 'make build') first"; \
	fi
endef

# install never builds: it links what is already there, and says what is missing
# instead of starting a build that was not asked for.
install:
	@if [ ! -x "$(shell pwd)/bin/niq" ]; then \
		echo "install: ./bin/niq is missing — run 'make build' first"; exit 1; fi
	$(install_steps)
	$(install_app_steps)
