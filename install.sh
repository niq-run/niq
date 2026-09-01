#!/usr/bin/env sh
# niq one-line installer: downloads a prebuilt binary from GitHub Releases
# into ~/.niq/bin (or $NIQ_INSTALL_DIR) and prints PATH instructions.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/install.sh | sh
#
# Options (env vars):
#   NIQ_INSTALL_DIR  target directory (default: ~/.niq/bin)
#   NIQ_VERSION      exact release tag to install (default: latest)

set -eu

REPO="niq-run/niq"
INSTALL_DIR="${NIQ_INSTALL_DIR:-$HOME/.niq/bin}"
VERSION="${NIQ_VERSION:-}"

log() { echo "[niq-install] $*"; }
fail() { echo "[niq-install] error: $*" >&2; exit 1; }

# --- detect platform (Go naming) ---
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os" in
  linux|darwin) ;;
  *) fail "unsupported OS: $os. Use npm: npm install -g @niq.run/niq" ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

# --- pick version ---
if [ -z "$VERSION" ]; then
  log "fetching latest release..."
  if command -v curl >/dev/null 2>&1; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep -o '"tag_name": *"[^"]*"' | head -n1 | sed 's/.*"v\{0,1\}\([^"]*\)"/\1/')
  else
    VERSION=$(wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep -o '"tag_name": *"[^"]*"' | head -n1 | sed 's/.*"v\{0,1\}\([^"]*\)"/\1/')
  fi
fi
[ -n "$VERSION" ] || fail "could not determine latest version"
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac

ASSET="niq_${os}_${arch}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"

log "installing niq ${VERSION} (${os}/${arch})"

# --- download & extract ---
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl -fSL "$URL" -o "$tmp/$ASSET"
else
  wget -qO "$tmp/$ASSET" "$URL"
fi

tar -xzf "$tmp/$ASSET" -C "$tmp"

# --- install ---
mkdir -p "$INSTALL_DIR"
mv "$tmp/niq" "$INSTALL_DIR/niq"
chmod +x "$INSTALL_DIR/niq"

log "installed: $INSTALL_DIR/niq"
"$INSTALL_DIR/niq" --version || true

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) log "NOTE: add $INSTALL_DIR to your PATH, e.g.:"
     echo "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc   # or ~/.bashrc" ;;
esac
