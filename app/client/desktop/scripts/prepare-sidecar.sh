#!/bin/sh
# Copy the CLI into src-tauri/binaries/ under the name Tauri expects.
#
# Tauri's `externalBin: ["binaries/niq"]` requires one file per target triple,
# e.g. binaries/niq-aarch64-apple-darwin. This script builds the CLI from this
# repository (or copies NIQ_BIN) and names it accordingly.
#
#   ./scripts/prepare-sidecar.sh            build ./bin/niq and bundle it
#   NIQ_BIN=/path/to/niq ./scripts/prepare-sidecar.sh   bundle that binary
#
# Run it before `cargo tauri build` (the Makefile's `desktop` target does).
set -eu

here=$(cd "$(dirname "$0")/.." && pwd)              # app/client/desktop
root=$(cd "$here/../../.." && pwd)                  # repository root

# A GUI-launched or CI shell may not have ~/.cargo/bin on PATH, so resolve the
# toolchain explicitly before giving up.
if command -v rustc >/dev/null 2>&1; then
	rustc_bin=rustc
elif [ -x "$HOME/.cargo/bin/rustc" ]; then
	rustc_bin="$HOME/.cargo/bin/rustc"
else
	echo "prepare-sidecar: rustc is required — install Rust first (https://rustup.rs)" >&2
	exit 1
fi

triple=$("$rustc_bin" --print host-tuple 2>/dev/null || "$rustc_bin" -Vv | sed -n 's/host: //p')
if [ -z "$triple" ]; then
	echo "prepare-sidecar: cannot determine the target triple from rustc" >&2
	exit 1
fi

ext=""
case "$triple" in
*windows*) ext=".exe" ;;
esac

mkdir -p "$here/src-tauri/binaries"
dest="$here/src-tauri/binaries/niq-$triple$ext"

if [ -n "${NIQ_BIN:-}" ]; then
	cp "$NIQ_BIN" "$dest"
else
	# -s -w strips the symbol table and DWARF: 30 MB -> 21 MB, matching what
	# the goreleaser build ships.
	echo "prepare-sidecar: building ./bin/niq ($triple)…"
	(cd "$root" && go build -trimpath -ldflags='-s -w' -o "$dest" ./app/publish/cmd/)
fi

chmod +x "$dest"
echo "prepare-sidecar: $dest"
