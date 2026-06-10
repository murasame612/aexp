#!/bin/sh
set -eu

REPO="${AEXP_REPO:-murasame612/aexp}"
VERSION="${AEXP_VERSION:-latest}"
INSTALL_DIR="${AEXP_INSTALL_DIR:-$HOME/.local/bin}"
BINARY_NAME="${AEXP_BINARY_NAME:-aexp}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: missing required command: $1" >&2
    exit 1
  fi
}

need curl
need tar

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "$os" in
  darwin|linux) ;;
  *)
    echo "error: unsupported OS: $os" >&2
    exit 1
    ;;
esac

case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "error: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

asset="aexp_${os}_${arch}.tar.gz"
if [ "$VERSION" = "latest" ]; then
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
  checksums_url="https://github.com/${REPO}/releases/latest/download/checksums.txt"
else
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  checksums_url="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading ${url}"
curl -fL "$url" -o "$tmp/$asset"

if command -v shasum >/dev/null 2>&1; then
  if curl -fsL "$checksums_url" -o "$tmp/checksums.txt"; then
    expected="$(grep "  ${asset}$" "$tmp/checksums.txt" | awk '{print $1}')"
    actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
    if [ -n "$expected" ] && [ "$expected" != "$actual" ]; then
      echo "error: checksum mismatch for ${asset}" >&2
      exit 1
    fi
  fi
fi

tar -xzf "$tmp/$asset" -C "$tmp"
if [ ! -x "$tmp/aexp" ]; then
  echo "error: archive did not contain executable ./aexp" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/aexp" "$INSTALL_DIR/$BINARY_NAME"
ln -sf "$BINARY_NAME" "$INSTALL_DIR/aexp-event"

echo "Installed $BINARY_NAME to $INSTALL_DIR/$BINARY_NAME"
echo
echo "Next steps:"
echo "  $INSTALL_DIR/$BINARY_NAME init"
echo "  $INSTALL_DIR/$BINARY_NAME serve --port 8080"
echo "  $INSTALL_DIR/$BINARY_NAME mcp install --target all"
