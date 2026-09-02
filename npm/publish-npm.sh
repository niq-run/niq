#!/usr/bin/env bash
# Generate and publish npm packages from a goreleaser dist/ directory.
#
# Usage: ./npm/publish-npm.sh <version> <dist-dir> [--dry-run]
#
# Layout produced:
#   <dist>/npm/@niq.run/niq-<os>-<arch>/   platform subpackages (binary only)
#   <dist>/npm/niq/                     main package (copied from npm/)
#
# goreleaser archives are named niq_<os>_<arch>.tar.gz where
# os/arch are Go names; they are mapped to npm os/arch names.

set -euo pipefail

VERSION="${1:?usage: publish-npm.sh <version> <dist-dir> [--dry-run]}"
DIST="${2:?usage: publish-npm.sh <version> <dist-dir> [--dry-run]}"
DRY_RUN="${3:-}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${DIST}/npm"

# goos -> npm os, goarch -> npm arch
os_map() {
  case "$1" in
    darwin) echo darwin ;;
    linux)  echo linux ;;
    windows) echo win32 ;;
    *) echo "" ;;
  esac
}
arch_map() {
  case "$1" in
    amd64) echo x64 ;;
    arm64) echo arm64 ;;
    *) echo "" ;;
  esac
}

rm -rf "${OUT}"
mkdir -p "${OUT}"

# 1. Platform subpackages from goreleaser archives.
for archive in "${DIST}"/niq_*_*.tar.gz "${DIST}"/niq_*_*.zip; do
  [ -e "$archive" ] || continue
  base="$(basename "$archive")"
  # niq_darwin_arm64.tar.gz -> goos=darwin goarch=arm64
  goos="$(echo "$base" | cut -d_ -f2)"
  goarch="$(echo "$base" | cut -d_ -f3)"
  goarch="${goarch%%.*}"   # strip .tar.gz / .zip suffix"
  nos="$(os_map "$goos")"
  narch="$(arch_map "$goarch")"
  if [ -z "$nos" ] || [ -z "$narch" ]; then
    echo "skip unsupported: $base"
    continue
  fi

  pkgdir="${OUT}/@niq.run/niq-${nos}-${narch}"
  mkdir -p "${pkgdir}/bin"
  if [[ "$base" == *.zip ]]; then
    unzip -j -o "$archive" 'niq.exe' -d "${pkgdir}/bin/" >/dev/null
  else
    tar -xzf "$archive" -C "${pkgdir}/bin/"
  fi

  cat > "${pkgdir}/package.json" <<EOF
{
  "name": "@niq.run/niq-${nos}-${narch}",
  "version": "${VERSION}",
  "description": "niq binary for ${nos}-${narch}",
  "license": "MIT",
  "os": ["${nos}"],
  "cpu": ["${narch}"],
  "files": ["bin/"]
}
EOF
done

# 2. Main package: copy npm/ and stamp version + matching optionalDeps.
mkdir -p "${OUT}/niq"
mkdir -p "${OUT}/niq/bin"
cp "${ROOT}/npm/bin/niq.js" "${OUT}/niq/bin/"
cp "${ROOT}/npm/README.md" "${OUT}/niq/README.md"
node - "${VERSION}" "${OUT}" "${ROOT}" <<'EOF'
const [version, out, root] = process.argv.slice(2);
const fs = require('fs');
const pkg = JSON.parse(fs.readFileSync(`${root}/npm/package.json`, 'utf8'));
pkg.version = version;
for (const dep of Object.keys(pkg.optionalDependencies || {})) {
  pkg.optionalDependencies[dep] = version;
}
fs.writeFileSync(`${out}/niq/package.json`, JSON.stringify(pkg, null, 2) + '\n');
EOF

echo "Generated npm packages under ${OUT}:"
find "${OUT}" -name package.json -maxdepth 3

if [ "$DRY_RUN" = "--dry-run" ]; then
  echo "(dry run, not publishing)"
  exit 0
fi

REGISTRY="https://registry.npmjs.org"

# published "<name>@<version>" → 0 if that exact version already exists on the
# registry, so re-runs (CI retries, tag re-pushes) skip instead of failing with
# EPUBLISHALREADY.
published() {
  npm view "$1" version --registry="$REGISTRY" >/dev/null 2>&1
}

for pkgjson in "${OUT}"/@niq.run/*/package.json "${OUT}"/niq/package.json; do
  dir="$(dirname "$pkgjson")"
  name="$(cd "$dir" && node -p "JSON.parse(require('fs').readFileSync('package.json','utf8')).name")"
  ver="$(cd "$dir" && node -p "JSON.parse(require('fs').readFileSync('package.json','utf8')).version")"
  if published "${name}@${ver}"; then
    echo "skip ${name}@${ver} (already published)"
    continue
  fi
  echo "publishing ${name}@${ver}"
  (cd "$dir" && npm publish --access public --registry="$REGISTRY")
done

echo "done."
