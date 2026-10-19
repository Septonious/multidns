#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
: "${ANDROID_HOME:?Set ANDROID_HOME to the Android SDK}"
export GOFLAGS="${GOFLAGS:-} -buildvcs=false"
export ANDROID_NDK_HOME="$ANDROID_HOME/ndk/28.0.13004108"
HEV_COMMIT=9341d64774c2bdaf6791931c8a6b41575031749f
MOBILE_VERSION=v0.0.0-20260204172633-1dceadbbeea3
mkdir -p build-deps android/app/libs
if [ ! -d build-deps/hev/.git ]; then
 git init build-deps/hev
 git -C build-deps/hev remote add origin https://github.com/heiher/hev-socks5-tunnel.git
fi
if [ "$(git -C build-deps/hev rev-parse HEAD 2>/dev/null || true)" != "$HEV_COMMIT" ]; then
 git -C build-deps/hev fetch --depth 1 origin "$HEV_COMMIT"
 git -C build-deps/hev checkout --detach "$HEV_COMMIT"
fi
git -C build-deps/hev submodule update --init --recursive --depth 1
go install "golang.org/x/mobile/cmd/gomobile@$MOBILE_VERSION"
go install "golang.org/x/mobile/cmd/gobind@$MOBILE_VERSION"
export PATH="$(go env GOPATH)/bin:$PATH"
gomobile bind -target=android/arm64,android/amd64 -androidapi=24 \
 -ldflags='-s -w -extldflags=-Wl,-z,max-page-size=16384' \
 -o android/app/libs/multidns.aar ./mobile
mkdir -p android/app/src/main/assets/licenses
cp build-deps/hev/LICENSE android/app/src/main/assets/licenses/hev.txt
cp build-deps/hev/src/core/LICENSE android/app/src/main/assets/licenses/hev-socks5-core.txt
cp build-deps/hev/third-part/hev-task-system/LICENSE android/app/src/main/assets/licenses/hev-task-system.txt
cp build-deps/hev/third-part/lwip/LICENSE android/app/src/main/assets/licenses/lwip.txt
cp build-deps/hev/third-part/yaml/LICENSE android/app/src/main/assets/licenses/yaml.txt
cd android
tasks=(assembleDebug lintDebug)
if [ "${CLEAN_ANDROID:-0}" = 1 ]; then
 tasks=(clean "${tasks[@]}")
fi
./gradlew --no-daemon "${tasks[@]}"
python3 ../scripts/check-android-native.py app/build/outputs/apk/debug/app-debug.apk
