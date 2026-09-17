#!/usr/bin/env sh
# Cross-compile ClaudePulse for every supported platform into dist/.
# Usage: scripts/build-all.sh [version]
set -e
cd "$(dirname "$0")/.."
VERSION="${1:-dev}"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  out="dist/claudepulse-${os}-${arch}"
  [ "$os" = "windows" ] && out="$out.exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" ./cmd/claudepulse
  echo "built $out"
done
