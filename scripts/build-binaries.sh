#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist
for arch in amd64 arm64; do
 for command in server client scanner; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "dist/multidns-$command-linux-$arch" "./cmd/$command"
 done
done
cd dist
sha256sum multidns-* > SHA256SUMS
