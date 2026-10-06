#!/usr/bin/env bash
# Builds the Go relay core into android/app/libs/opentether-core.aar.
#
# Requires: Go, a JDK, the Android SDK (ANDROID_HOME) and NDK (ANDROID_NDK_HOME,
# or an NDK installed under $ANDROID_HOME/ndk).
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -z "${ANDROID_NDK_HOME:-}" && -n "${ANDROID_HOME:-}" && -d "$ANDROID_HOME/ndk" ]]; then
  ANDROID_NDK_HOME="$(ls -d "$ANDROID_HOME"/ndk/* | sort -V | tail -1)"
  export ANDROID_NDK_HOME
fi

go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest
export PATH="$(go env GOPATH)/bin:$PATH"

mkdir -p android/app/libs
gomobile bind \
  -target=android/arm64,android/arm,android/amd64 \
  -androidapi 29 \
  -javapkg org.opentether.core \
  -ldflags="-s -w" \
  -trimpath \
  -o android/app/libs/opentether-core.aar \
  ./mobile

echo "Built android/app/libs/opentether-core.aar"
