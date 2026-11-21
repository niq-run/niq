#!/bin/sh
# Packaging for the macOS distribution.
#
# The interface lives in app/client/menubar (Swift + the bundle's plist and
# icon); this script turns it into a download: compile the app, assemble
# niq.app, and, for a release, lipo a universal binary and zip it with the miq
# launcher.
#
#   build.sh             a .app for this machine, plus ./bin/miq
#   build.sh universal   one .app for arm64 and x86_64
#   build.sh zip         universal + dist/niq_menubar_darwin_universal.zip
set -eu

root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"

src=app/client/menubar
app=dist/niq.app
min_macos=13.0
zip_name=niq_menubar_darwin_universal.zip

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
		-o "$app/Contents/MacOS/niq-menubar"
	install_launcher
	echo "app: built — run: ./bin/miq"
	;;
universal)
	echo "app: building $app (universal)…"
	assemble
	rm -f build/niq-menubar-*
	mkdir -p build
	swiftc -O -swift-version 5 -target "arm64-apple-macos$min_macos" -framework AppKit \
		"$src/main.swift" -o build/niq-menubar-arm64
	swiftc -O -swift-version 5 -target "x86_64-apple-macos$min_macos" -framework AppKit \
		"$src/main.swift" -o build/niq-menubar-x86_64
	lipo -create -output "$app/Contents/MacOS/niq-menubar" \
		build/niq-menubar-arm64 build/niq-menubar-x86_64
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
	rm -rf dist/niq-menubar
	mkdir -p dist/niq-menubar
	cp -R "$app" dist/niq-menubar/
	cp ./bin/miq dist/niq-menubar/
	# --norsrc/--noextattr: no AppleDouble ._ entries in the archive
	(cd dist && ditto -c -k --norsrc --noextattr --noqtn --noacl \
		--keepParent niq-menubar "$zip_name")
	echo "app: dist/$zip_name"
	;;
*)
	echo "usage: $0 [build|universal|zip]" >&2
	exit 2
	;;
esac
