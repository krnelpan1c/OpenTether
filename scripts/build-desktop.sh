#!/usr/bin/env bash
# Builds the desktop client and dev relay into dist/<os>-<arch>/.
# Usage: scripts/build-desktop.sh [goos] [goarch]   (defaults to the host)
set -euo pipefail

cd "$(dirname "$0")/.."
GOOS="${1:-$(go env GOOS)}"
GOARCH="${2:-$(go env GOARCH)}"
VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
OUT="dist/${GOOS}-${GOARCH}"
EXT=""
[[ "$GOOS" == "windows" ]] && EXT=".exe"

mkdir -p "$OUT"
for cmd in opentether opentether-relay; do
  GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o "${OUT}/${cmd}${EXT}" "./cmd/${cmd}"
done

if [[ "$GOOS" == "windows" ]]; then
  # Wintun provides the virtual adapter; its signed DLL must sit next to the exe.
  WINTUN_ZIP="$(mktemp -d)/wintun.zip"
  curl -fsSL -o "$WINTUN_ZIP" https://www.wintun.net/builds/wintun-0.14.1.zip
  unzip -o -j -q "$WINTUN_ZIP" "wintun/bin/${GOARCH}/wintun.dll" -d "$OUT"
  # libusb drives USB accessory mode; only an amd64 build is published.
  if [[ "$GOARCH" == "amd64" ]]; then
    LIBUSB_DIR="$(mktemp -d)"
    curl -fsSL -o "$LIBUSB_DIR/libusb.7z" https://github.com/libusb/libusb/releases/download/v1.0.30/libusb-1.0.30.7z
    tar -xf "$LIBUSB_DIR/libusb.7z" -C "$LIBUSB_DIR" VS2019/MS64/dll/libusb-1.0.dll
    cp "$LIBUSB_DIR/VS2019/MS64/dll/libusb-1.0.dll" "$OUT/"
  fi
fi

echo "Built ${OUT}"
