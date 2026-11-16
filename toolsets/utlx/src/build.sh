#!/usr/bin/env bash
# Builds the utlx toolset into dist/: the Go tool binary, the manifest, and the
# utl-x CLI as bin/utlx (the tools shell out to it).
#
# Invoked by `sam config apply`, `sam toolset package` and `sam toolset validate`,
# which export SAM_TOOL_TARGET_OS / SAM_TOOL_TARGET_ARCH for the Secure Tool
# Runtime (STR) the toolset will run on. For a manual build export both, e.g.
#   SAM_TOOL_TARGET_OS=linux SAM_TOOL_TARGET_ARCH=amd64 ./build.sh
#
# Where bin/utlx comes from:
#   UTLX_BINARY=/path/to/utlx   use this binary (it must match the target)
#   otherwise                    download the utl-x release for the target:
#                                  linux/amd64  -> utlx-linux-x64
#                                  darwin/arm64 -> utlx-macos-arm64 (host validate only)
#                                linux/arm64 has no release binary: build one with
#                                ../tools/build-utlx-linux-arm64.sh and pass UTLX_BINARY.
#   UTLX_VERSION                 release tag without "v" (default 1.3.1)
#   UTLX_SHA256                  optional checksum the binary must match
#   UTLX_SKIP_BINARY=1           build without bin/utlx (schema discovery still works;
#                                the tools then need utlx on PATH or the utlx_path config)
set -euo pipefail

OUT_DIR="${SAM_TOOL_BUILD_OUT:-dist}"
NAME="${SAM_TOOL_NAME:-utlx}"
: "${SAM_TOOL_TARGET_OS:?must be set by sam config apply, or exported for a manual build (e.g. linux)}"
: "${SAM_TOOL_TARGET_ARCH:?must be set by sam config apply, or exported for a manual build (e.g. amd64)}"
TARGET_OS="$SAM_TOOL_TARGET_OS"
TARGET_ARCH="$SAM_TOOL_TARGET_ARCH"
UTLX_VERSION="${UTLX_VERSION:-1.3.1}"
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CACHE_DIR="${UTLX_CACHE_DIR:-$SRC_DIR/../.sam-cache/utlx}"

mkdir -p "$OUT_DIR"

# 1. The Go tool binary.
CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" \
  go build -trimpath -buildvcs=false -o "$OUT_DIR/$NAME" .

# 2. The manifest, so the STR can discover the tools.
cp manifest.yaml "$OUT_DIR/manifest.yaml"

# 3. The utl-x CLI as bin/utlx.
if [[ "${UTLX_SKIP_BINARY:-}" == "1" ]]; then
  echo "build.sh: UTLX_SKIP_BINARY=1, not bundling utl-x" >&2
  exit 0
fi

src=""
if [[ -n "${UTLX_BINARY:-}" ]]; then
  [[ -f "$UTLX_BINARY" ]] || { echo "build.sh: UTLX_BINARY=$UTLX_BINARY not found" >&2; exit 1; }
  src="$UTLX_BINARY"
else
  case "$TARGET_OS/$TARGET_ARCH" in
    linux/amd64)  asset="utlx-linux-x64" ;;
    darwin/arm64) asset="utlx-macos-arm64" ;;
    *)
      cat >&2 <<EOF
build.sh: utl-x $UTLX_VERSION has no release binary for $TARGET_OS/$TARGET_ARCH.
  Build one (linux/arm64): ../tools/build-utlx-linux-arm64.sh
  then rerun with UTLX_BINARY=/path/to/utlx
  or set UTLX_SKIP_BINARY=1 and provide utlx in the STR image (utlx_path config).
EOF
      exit 1 ;;
  esac
  src="$CACHE_DIR/$UTLX_VERSION/$asset"
  if [[ ! -s "$src" ]]; then
    mkdir -p "$(dirname "$src")"
    url="https://github.com/grauwen/utl-x/releases/download/v$UTLX_VERSION/$asset"
    echo "build.sh: downloading $url" >&2
    curl -fsSL --retry 3 -o "$src.part" "$url"
    mv "$src.part" "$src"
  fi
fi

if [[ -n "${UTLX_SHA256:-}" ]]; then
  actual="$( (command -v sha256sum >/dev/null && sha256sum "$src" || shasum -a 256 "$src") | awk '{print $1}')"
  [[ "$actual" == "$UTLX_SHA256" ]] || { echo "build.sh: utl-x checksum mismatch ($actual)" >&2; exit 1; }
fi

mkdir -p "$OUT_DIR/bin"
cp "$src" "$OUT_DIR/bin/utlx"
chmod 0755 "$OUT_DIR/bin/utlx"
