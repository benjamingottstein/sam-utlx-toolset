#!/usr/bin/env bash
# Builds a linux/arm64 utl-x native binary (utl-x publishes linux-x64 and
# macos-arm64 only). Needs Docker or Podman; the build runs GraalVM
# native-image inside a linux/arm64 container and takes several minutes and
# ~8 GB RAM.
#
#   ./build-utlx-linux-arm64.sh [version]      # default 1.3.1
#   -> ./out/utlx-linux-arm64
#
# Then package with:
#   UTLX_BINARY=$PWD/out/utlx-linux-arm64 sam toolset package utlx ../../.. --url <platform>
set -euo pipefail

VERSION="${1:-1.3.1}"
ENGINE="${CONTAINER_ENGINE:-$(command -v docker || command -v podman)}"
[[ -n "$ENGINE" ]] || { echo "need docker or podman" >&2; exit 1; }
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$HERE/out"

"$ENGINE" run --rm --platform linux/arm64 \
  -v "$HERE/out:/out" \
  -e VERSION="$VERSION" \
  ghcr.io/graalvm/native-image-community:21 \
  bash -euo pipefail -c '
    microdnf install -y git findutils >/dev/null
    git clone --depth 1 --branch "v$VERSION" https://github.com/grauwen/utl-x /src
    cd /src
    ./gradlew --no-daemon :modules:cli:nativeCompile
    cp modules/cli/build/native/nativeCompile/utlx /out/utlx-linux-arm64
  '

file "$HERE/out/utlx-linux-arm64" 2>/dev/null || true
echo "built $HERE/out/utlx-linux-arm64"
