#!/usr/bin/env bash
# Builds the desktop client and dev relay into dist/<os>-<arch>/ for every
# supported desktop platform. Windows builds also get the signed wintun.dll
# the virtual adapter needs (and libusb for the experimental accessory mode).
#
#   scripts/build-desktop.sh                          # all targets
#   scripts/build-desktop.sh linux arm64              # just one
#   scripts/build-desktop.sh linux/arm64 darwin/arm64 # a few
set -euo pipefail

cd "$(dirname "$0")/.."

ALL_TARGETS=(windows/amd64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)
SUPPORTED=" windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 "

if [[ $# -eq 0 ]]; then
  TARGETS=("${ALL_TARGETS[@]}")
elif [[ $# -eq 2 && "$1" != */* ]]; then
  TARGETS=("$1/$2") # old "goos goarch" form
else
  TARGETS=("$@")
fi

VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
# bsdtar (libarchive) reads both zip and 7z; GNU tar reads neither. macOS tar
# and Windows' System32 tar.exe are bsdtar.
if command -v bsdtar >/dev/null; then TAR=bsdtar
elif [[ -x /c/Windows/System32/tar.exe ]]; then TAR=/c/Windows/System32/tar.exe
else TAR=tar; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# fetch URL NAME: download and extract an archive once per run.
fetch() {
  local dir="$TMP/$2"
  if [[ ! -d "$dir" ]]; then
    mkdir -p "$dir"
    echo "  downloading $(basename "$1")..." >&2
    curl -fsSL -o "$dir/archive" "$1"
    if [[ "$TAR" == /c/Windows/* ]]; then
      "$TAR" -xf "$(cygpath -w "$dir/archive")" -C "$(cygpath -w "$dir")" # Windows paths
    else
      "$TAR" -xf "$dir/archive" -C "$dir"
    fi
  fi
  echo "$dir"
}

for target in "${TARGETS[@]}"; do
  if [[ "$SUPPORTED" != *" $target "* ]]; then
    echo "unsupported target '$target' (choose from:$SUPPORTED)" >&2
    exit 1
  fi
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  OUT="dist/${GOOS}-${GOARCH}"
  EXT=""
  [[ "$GOOS" == "windows" ]] && EXT=".exe"
  mkdir -p "$OUT"

  echo "Building ${target} (${VERSION})..."
  for cmd in opentether opentether-relay; do
    GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o "${OUT}/${cmd}${EXT}" "./cmd/${cmd}"
  done

  if [[ "$GOOS" == "windows" ]]; then
    # Wintun provides the virtual adapter; its signed DLL must sit next to the exe.
    if [[ ! -f "$OUT/wintun.dll" ]]; then
      dir="$(fetch https://www.wintun.net/builds/wintun-0.14.1.zip wintun)"
      cp "$dir/wintun/bin/${GOARCH}/wintun.dll" "$OUT/"
    fi
    # libusb is only used by the experimental accessory mode; x64 only.
    if [[ "$GOARCH" == "amd64" && ! -f "$OUT/libusb-1.0.dll" ]]; then
      dir="$(fetch https://github.com/libusb/libusb/releases/download/v1.0.30/libusb-1.0.30.7z libusb)"
      cp "$dir/VS2019/MS64/dll/libusb-1.0.dll" "$OUT/"
    fi
  fi
  echo "  -> ${OUT}"
done

echo "Run the client as Administrator (Windows) or with sudo (Linux/macOS), e.g.: opentether usb --stats"
echo "macOS builds are unsigned: clear the quarantine flag with: xattr -d com.apple.quarantine opentether"
