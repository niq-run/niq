#!/bin/sh
# Release artifacts for the desktop shell, on macOS, Windows and Linux.
#
# The app itself is built by its own project (app/client/desktop, a Tauri crate).
# This script is the *distribution* half: stamp a version, stage the CLI as a
# sidecar (one per architecture for a universal macOS build), run the bundle
# build, and collect the artifacts under dist/ with stable, platform-suffixed
# names.
#
#   release.sh                          build for this machine
#   release.sh --universal              macOS only: arm64 + x86_64 in one bundle
#   release.sh --version 0.18.0         stamp tauri.conf.json + Cargo.toml first
#   release.sh --out /tmp/artifacts     stage somewhere other than ./dist
#
# Artifacts (one line per produced file):
#   niq-desktop_<version>_macos_<arch>.dmg / .app.zip
#   niq-desktop_<version>_windows_<arch>.msi / _setup.exe
#   niq-desktop_<version>_linux_<arch>.AppImage / .deb / .rpm
#
# macOS signing/notarisation: APPLE_SIGNING_IDENTITY (codesign) and
# APPLE_ID + APPLE_PASSWORD + APPLE_TEAM_ID (notarytool). Without them the bundle
# is ad-hoc signed: fine locally, not for distribution. Windows signing is
# configured in tauri.conf.json / CI secrets; Linux bundles are unsigned.
set -eu

usage() {
	cat <<'EOF'
usage: release.sh [--universal] [--version X.Y.Z] [--out DIR]

  --universal       macOS: build for apple silicon and Intel (needs both rust targets)
  --version X.Y.Z   stamp tauri.conf.json and Cargo.toml before building
  --out DIR         where to stage artifacts (default: <repo>/dist)

env:
  APPLE_SIGNING_IDENTITY          macOS codesign identity
  APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID
                                  macOS notarisation credentials
EOF
}

version=""
universal=0
out=""
while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || { echo "release: --version needs a value" >&2; exit 2; }
		version=$2
		shift 2
		;;
	--universal)
		universal=1
		shift
		;;
	--out)
		[ $# -ge 2 ] || { echo "release: --out needs a value" >&2; exit 2; }
		out=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "release: unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
client="$root/app/client/desktop"
conf="$client/src-tauri/tauri.conf.json"
manifest="$client/src-tauri/Cargo.toml"
[ -n "$out" ] || out="$root/dist"

# ── Platform ────────────────────────────────────────────────────────────────
case "$(uname -s)" in
Darwin) os=macos ;;
Linux) os=linux ;;
MINGW* | MSYS* | CYGWIN* | Windows*) os=windows ;;
*)
	echo "release: unsupported platform: $(uname -s)" >&2
	exit 1
	;;
esac

if [ "$os" != "macos" ] && [ "$universal" = "1" ]; then
	echo "release: --universal is macOS only" >&2
	exit 2
fi

case "$(uname -m)" in
arm64 | aarch64) arch=arm64 ;;
x86_64 | amd64) arch=x64 ;;
*) arch=$(uname -m) ;;
esac
[ "$universal" = "1" ] && arch=universal

# ── Toolchain (resolved past PATH: a GUI/CI shell may lack ~/.cargo/bin) ────
if command -v cargo >/dev/null 2>&1; then
	cargo_bin=cargo
elif [ -x "$HOME/.cargo/bin/cargo" ]; then
	cargo_bin="$HOME/.cargo/bin/cargo"
else
	echo "release: cargo is required — install Rust (https://rustup.rs)" >&2
	exit 1
fi

# The Tauri CLI shells out to `cargo metadata`, so cargo must be on PATH.
PATH="$(dirname "$cargo_bin"):$PATH"
export PATH

if command -v tauri >/dev/null 2>&1; then
	tauri_bin=$(command -v tauri)
elif "$cargo_bin" tauri --version >/dev/null 2>&1; then
	tauri_bin=""
else
	echo "release: the Tauri CLI is required — npm i -g @tauri-apps/cli" >&2
	exit 1
fi

run_tauri() {
	if [ -n "$tauri_bin" ]; then
		(cd "$client" && "$tauri_bin" build "$@")
	else
		(cd "$client" && "$cargo_bin" tauri build "$@")
	fi
}

# ── Version ────────────────────────────────────────────────────────────────
# Pure sed, no python: the Windows runner's Git Bash does not guarantee a
# `python3`, and this has to work there.
stamp() { # stamp <file> <sed-expression>
	tmp="$1.niq-tmp"
	sed "$2" "$1" > "$tmp" && mv "$tmp" "$1"
}

if [ -n "$version" ]; then
	echo "release: stamping version $version"
	stamp "$conf" 's/^\( *"version": "\)[^"]*\(".*\)$/\1'"$version"'\2/'
	stamp "$manifest" 's/^\(version = "\)[^"]*\(".*\)$/\1'"$version"'\2/'
	app_version=$version
else
	app_version=$(sed -n 's/^ *"version": "\([^"]*\)".*/\1/p' "$conf" | head -1)
	[ -n "$app_version" ] || { echo "release: cannot read the version from $conf" >&2; exit 1; }
fi
echo "release: version $app_version"

# ── Sidecar ────────────────────────────────────────────────────────────────
mkdir -p "$client/src-tauri/binaries"

if [ "$universal" = "1" ]; then
	# Tauri builds each architecture for its own triple and then bundles both into
	# one app, so it needs all three names: one sidecar per triple while each arch
	# compiles, plus the lipo'd binary the bundling step copies in.
	echo "release: building the per-architecture CLI sidecars…"
	rustup target add aarch64-apple-darwin x86_64-apple-darwin >/dev/null
	(cd "$root" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' \
		-o "$client/src-tauri/binaries/niq-aarch64-apple-darwin" ./app/publish/cmd/)
	(cd "$root" && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
		-o "$client/src-tauri/binaries/niq-x86_64-apple-darwin" ./app/publish/cmd/)
	lipo -create -output "$client/src-tauri/binaries/niq-universal-apple-darwin" \
		"$client/src-tauri/binaries/niq-aarch64-apple-darwin" \
		"$client/src-tauri/binaries/niq-x86_64-apple-darwin"
	echo "release: sidecars → binaries/niq-{aarch64,x86_64,universal}-apple-darwin"
elif [ -x "$root/bin/niq" ]; then
	(cd "$client" && NIQ_BIN="$root/bin/niq" ./scripts/prepare-sidecar.sh)
else
	(cd "$client" && ./scripts/prepare-sidecar.sh)
fi

# ── Bundle ─────────────────────────────────────────────────────────────────
if [ "$universal" = "1" ]; then
	echo "release: building the universal app…"
	run_tauri --target universal-apple-darwin
	bundle="$client/src-tauri/target/universal-apple-darwin/release/bundle"
else
	echo "release: building the app…"
	run_tauri
	bundle="$client/src-tauri/target/release/bundle"
fi

# ── Signing / notarisation (macOS) ─────────────────────────────────────────
if [ "$os" = "macos" ]; then
	if [ -n "${APPLE_SIGNING_IDENTITY:-}" ]; then
		echo "release: bundle signed by Tauri as ${APPLE_SIGNING_IDENTITY}"
	else
		echo "release: no APPLE_SIGNING_IDENTITY — ad-hoc signed (local use only)"
	fi
fi

# ── Stage ──────────────────────────────────────────────────────────────────
mkdir -p "$out"
name="niq-desktop_${app_version}_${os}_${arch}"
staged=0

stage() { # stage <source> <extension>
	[ -f "$1" ] || return 0
	cp "$1" "$out/$name.$2"
	echo "release: $out/$name.$2"
	staged=$((staged + 1))
}

first() { # first <glob> — the newest match, empty when there is none
	ls -t $1 2>/dev/null | head -1 || true
}

case "$os" in
macos)
	dmg=$(first "$bundle/dmg/*.dmg")
	app="$bundle/macos/niq.app"
	if [ ! -d "$app" ]; then
		echo "release: no app bundle at $app" >&2
		exit 1
	fi
	if [ -n "$dmg" ] && [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_PASSWORD:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ]; then
		echo "release: notarising $dmg…"
		xcrun notarytool submit "$dmg" \
			--apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID" --wait
		xcrun stapler staple "$dmg"
		xcrun stapler staple "$app"
	else
		echo "release: skipping notarisation (APPLE_ID / APPLE_PASSWORD / APPLE_TEAM_ID not all set)"
	fi

	# An ad-hoc bundle can never satisfy Gatekeeper, so nothing can make the app
	# open freely — the least the DMG can do is sign the bundle properly and say
	# so. A notarised build needs neither.
	if [ -z "${APPLE_SIGNING_IDENTITY:-}" ]; then
		# Tauri leaves the app with only the linker's signature on the Mach-O,
		# which is not a bundle signature: `codesign -v` rejects it with "code
		# has no resources but signature indicates they must be present", and
		# that is what macOS reports as a damaged app. Signing the bundle
		# ad-hoc seals Resources and makes the signature consistent.
		echo "release: ad-hoc build — signing the bundle and shipping INSTALL.txt"
		# Sign from the inside out: the CLI sidecar in Contents/MacOS is a Mach-O
		# with no signature of its own, and codesign refuses the bundle while a
		# subcomponent is unsigned ("code object is not signed at all / In
		# subcomponent: Contents/MacOS/niq"). Nested code first, the bundle last.
		for bin in "$app/Contents/MacOS"/* "$app/Contents/Frameworks"/*; do
			[ -f "$bin" ] || continue
			codesign --force --sign - "$bin" >/dev/null 2>&1 || \
				echo "release: could not sign $(basename "$bin")" >&2
		done
		if ! codesign --force --sign - "$app" || ! codesign -v "$app"; then
			echo "release: could not sign the bundle ad-hoc" >&2
		fi
		cp "$here/INSTALL.txt" "$out/$name.INSTALL.txt"
		echo "release: $out/$name.INSTALL.txt"
		staged=$((staged + 1))

		if [ -n "$dmg" ]; then
			# hdiutil cannot add a file to a compressed image: convert it to
			# read/write, put the signed app and the note in, convert back.
			work=$(mktemp -d)
			base=$(basename "$app")
			if hdiutil convert "$dmg" -format UDRW -o "$work/rw" >/dev/null 2>&1 \
				&& mkdir -p "$work/mnt" \
				&& hdiutil attach "$work/rw.dmg" -nobrowse -readwrite -mountpoint "$work/mnt" >/dev/null 2>&1
			then
				rm -rf "$work/mnt/$base"
				ditto "$app" "$work/mnt/$base" 2>/dev/null || true
				cp "$here/INSTALL.txt" "$work/mnt/INSTALL.txt" 2>/dev/null || true
				hdiutil detach "$work/mnt" >/dev/null 2>&1 || true
				if hdiutil convert "$work/rw.dmg" -format UDZO -o "$work/final" >/dev/null 2>&1; then
					dmg="$work/final.dmg"
				else
					echo "release: could not repack the dmg; shipping the original" >&2
				fi
			else
				echo "release: could not mount the dmg; shipping the original" >&2
			fi
		fi
	fi

	stage "$dmg" dmg
	if [ -n "${work:-}" ]; then rm -rf "$work"; fi
	rm -f "$out/$name.app.zip"
	ditto -c -k --norsrc --noextattr --noqtn --noacl --keepParent "$app" "$out/$name.app.zip"
	echo "release: $out/$name.app.zip"
	staged=$((staged + 1))
	;;
windows)
	stage "$(first "$bundle/msi/*.msi")" msi
	stage "$(first "$bundle/nsis/*-setup.exe")" setup.exe
	;;
linux)
	stage "$(first "$bundle/appimage/*.AppImage")" AppImage
	stage "$(first "$bundle/deb/*.deb")" deb
	stage "$(first "$bundle/rpm/*.rpm")" rpm
	;;
esac

if [ "$staged" = "0" ]; then
	echo "release: the build produced no recognizable artifact under $bundle" >&2
	exit 1
fi

cat <<EOF

release: done — $staged artifact(s) in $out
  bundle  $bundle

Reminder: a version stamp rewrites tauri.conf.json + Cargo.toml (and cargo will
update Cargo.lock) — commit those with the release.
EOF
