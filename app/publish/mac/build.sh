#!/bin/sh
# Packaging for the macOS distribution.
#
# The app lives in app/client/mac (Swift + the bundle's plist and icon);
# this script turns it into a download: compile the app, assemble niq.app, and,
# for a release, lipo a universal binary and zip it with the miq launcher.
#
#   build.sh             a .app for this machine, plus ./bin/miq
#   build.sh universal   one .app for arm64 and x86_64
#   build.sh zip         universal + dist/niq_mac_darwin_universal.zip
set -eu

root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"

src=app/client/mac
app=dist/niq.app
min_macos=13.0
zip_name=niq_mac_darwin_universal.zip

assemble() {
	rm -rf "$app"
	mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" bin
	cp "$src/Info.plist" "$app/Contents/Info.plist"
	cp "$src/AppIcon.icns" "$app/Contents/Resources/"
	printf 'APPL????' > "$app/Contents/PkgInfo"
}

install_launcher() {
	cp app/publish/mac/miq ./bin/miq
	chmod +x ./bin/miq
}

case "${1:-build}" in
build)
	echo "app: building $app (swiftc)…"
	assemble
	swiftc -O -swift-version 5 -framework AppKit "$src/main.swift" \
		-o "$app/Contents/MacOS/niq-mac"
	install_launcher
	echo "app: built — run: ./bin/miq"
	;;
universal)
	echo "app: building $app (universal)…"
	assemble
	# The two slices are lipo inputs, not artifacts: keep them under dist so
	# the source tree has no build/ directory, and drop them when done.
	tmp=dist/.build
	rm -rf "$tmp"
	mkdir -p "$tmp"
	swiftc -O -swift-version 5 -target "arm64-apple-macos$min_macos" -framework AppKit \
		"$src/main.swift" -o "$tmp/niq-mac-arm64"
	swiftc -O -swift-version 5 -target "x86_64-apple-macos$min_macos" -framework AppKit \
		"$src/main.swift" -o "$tmp/niq-mac-x86_64"
	lipo -create -output "$app/Contents/MacOS/niq-mac" \
		"$tmp/niq-mac-arm64" "$tmp/niq-mac-x86_64"
	rm -rf "$tmp"
	# lipo invalidates the linker's ad-hoc signature, and an unsigned arm64
	# binary will not run at all — re-sign the bundle. miq is a shell script
	# and has no architectures, so only the bundle needs signing.
	codesign --force --sign - --timestamp=none "$app"
	install_launcher
	echo "app: built (universal)"
	;;
zip)
	"$0" universal
	echo "app: packaging dist/${zip_name}"
	rm -rf dist/niq-mac
	mkdir -p dist/niq-mac
	cp -R "$app" dist/niq-mac/
	cp ./bin/miq dist/niq-mac/
	# --norsrc/--noextattr: no AppleDouble ._ entries in the archive
	(cd dist && ditto -c -k --norsrc --noextattr --noqtn --noacl \
		--keepParent niq-mac "$zip_name")
	rm -rf dist/niq-mac
	echo "app: dist/$zip_name"
	;;
*)
	echo "usage: $0 [build|universal|zip]" >&2
	exit 2
	;;
esac
