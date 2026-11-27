#!/bin/sh
# Release artifacts for the desktop shell.
#
# The app itself is built by its own project (app/client/desktop, a Tauri crate).
# This script is the *distribution* half: stamp a version, stage the CLI as a
# sidecar (lipo'd for a universal build), run the bundle build, and collect the
# artifacts under dist/ with stable names.
#
#   release.sh                        host build  → dist/niq-desktop_<ver>_<arch>.{dmg,app.zip}
#   release.sh --universal            arm64+x86_64 (lipo'd CLI + universal app)
#   release.sh --version 0.18.0       stamp tauri.conf.json + Cargo.toml first
#
# Signing: set APPLE_SIGNING_IDENTITY and the bundle is signed by Tauri.
# Notarisation: set APPLE_ID + APPLE_PASSWORD + APPLE_TEAM_ID (and, for CI,
# APPLE_API_KEY/APPLE_API_ISSUER) and the .dmg is submitted and stapled.
# Without either, the artifacts are ad-hoc signed — fine locally, not for
# distribution.
set -eu

usage() {
	cat <<'EOF'
usage: release.sh [--universal] [--version X.Y.Z]

  --universal       build for apple silicon and Intel (needs both rust targets)
  --version X.Y.Z   stamp tauri.conf.json and Cargo.toml before building

env:
  APPLE_SIGNING_IDENTITY          codesign identity for the bundle
  APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID
                                  notarisation credentials for the .dmg
EOF
}

version=""
universal=0
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
dist="$root/dist"
conf="$client/src-tauri/tauri.conf.json"
manifest="$client/src-tauri/Cargo.toml"

# Toolchain: resolve past PATH (a CI/GUI shell may not have ~/.cargo/bin).
if command -v cargo >/dev/null 2>&1; then
	cargo_bin=cargo
elif [ -x "$HOME/.cargo/bin/cargo" ]; then
	cargo_bin="$HOME/.cargo/bin/cargo"
else
	echo "release: cargo is required — install Rust (https://rustup.rs)" >&2
	exit 1
fi

# The Tauri CLI shells out to `cargo metadata`, so cargo must be on PATH (a
# GUI/CI shell may not have ~/.cargo/bin there).
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

# MARK: - Version

if [ -n "$version" ]; then
	echo "release: stamping version $version"
	python3 - "$conf" "$manifest" "$version" <<'PY'
import json, re, sys

conf_path, manifest_path, version = sys.argv[1], sys.argv[2], sys.argv[3]

with open(conf_path) as fh:
    conf = json.load(fh)
conf["version"] = version
with open(conf_path, "w") as fh:
    json.dump(conf, fh, indent=2, ensure_ascii=False)
    fh.write("\n")

with open(manifest_path) as fh:
    text = fh.read()
text, count = re.subn(r'(?m)^version = "[^"]*"', f'version = "{version}"', text, count=1)
if count != 1:
    sys.exit("could not find the package version in Cargo.toml")
with open(manifest_path, "w") as fh:
    fh.write(text)
PY
fi

if [ -n "$version" ]; then
	app_version=$version
else
	app_version=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$conf")
fi

# MARK: - Sidecar

mkdir -p "$client/src-tauri/binaries"

if [ "$universal" = "1" ]; then
	echo "release: building a universal CLI sidecar…"
	"$cargo_bin" --version >/dev/null
	# Both slices are needed for the universal app, and so are both rust targets.
	rustup target add aarch64-apple-darwin x86_64-apple-darwin >/dev/null
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	(cd "$root" && GOARCH=arm64 GOOS=darwin go build -o "$tmp/niq-arm64" ./app/publish/cmd/)
	(cd "$root" && GOARCH=amd64 GOOS=darwin go build -o "$tmp/niq-amd64" ./app/publish/cmd/)
	lipo -create -output "$client/src-tauri/binaries/niq-universal-apple-darwin" \
		"$tmp/niq-arm64" "$tmp/niq-amd64"
	echo "release: sidecar → binaries/niq-universal-apple-darwin"
else
	# Reuse the CLI that `make build` produced; build it if it is not there.
	if [ -x "$root/bin/niq" ]; then
		(cd "$client" && NIQ_BIN="$root/bin/niq" ./scripts/prepare-sidecar.sh)
	else
		(cd "$client" && ./scripts/prepare-sidecar.sh)
	fi
fi

# MARK: - Bundle

if [ "$universal" = "1" ]; then
	echo "release: building the universal app…"
	run_tauri --target universal-apple-darwin
	bundle="$client/src-tauri/target/universal-apple-darwin/release/bundle"
	arch=universal
else
	echo "release: building the app…"
	run_tauri
	bundle="$client/src-tauri/target/release/bundle"
	case "$(uname -m)" in
	arm64) arch=aarch64 ;;
	*) arch=$(uname -m) ;;
	esac
fi

app="$bundle/macos/niq.app"
dmg=$(ls "$bundle"/dmg/*.dmg 2>/dev/null | head -1 || true)

if [ ! -d "$app" ]; then
	echo "release: no app bundle at $app" >&2
	exit 1
fi

# MARK: - Signing / notarisation

if [ -n "${APPLE_SIGNING_IDENTITY:-}" ]; then
	echo "release: bundle signed by Tauri as ${APPLE_SIGNING_IDENTITY}"
else
	echo "release: no APPLE_SIGNING_IDENTITY — the bundle is ad-hoc signed (local use only)"
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

# MARK: - Stage artifacts

mkdir -p "$dist"
name="niq-desktop_${app_version}_${arch}"
if [ -n "$dmg" ]; then
	cp "$dmg" "$dist/$name.dmg"
	echo "release: $dist/$name.dmg"
fi
rm -f "$dist/$name.app.zip"
ditto -c -k --norsrc --noextattr --noqtn --noacl --keepParent "$app" "$dist/$name.app.zip"
echo "release: $dist/$name.app.zip"

cat <<EOF

release: done
  app    $app
  dist   $dist/$name.{dmg,app.zip}

Reminder: a version stamp rewrites tauri.conf.json + Cargo.toml (and cargo will
update Cargo.lock) — commit those with the release.
EOF
