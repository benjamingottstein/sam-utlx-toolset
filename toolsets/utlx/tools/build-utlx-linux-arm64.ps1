<#
.SYNOPSIS
  Builds a linux/arm64 utl-x native binary on Windows (utl-x publishes
  linux-x64, macos-arm64 and windows-x64 only). PowerShell twin of
  build-utlx-linux-arm64.sh.

.DESCRIPTION
  Runs GraalVM native-image inside a linux/arm64 container with Docker or
  Podman (Docker Desktop needs QEMU/binfmt emulation for arm64, which it ships
  by default). Takes several minutes and about 8 GB RAM.

.EXAMPLE
  .\build-utlx-linux-arm64.ps1            # default version 1.3.1
  .\build-utlx-linux-arm64.ps1 -Version 1.3.1
  # -> .\out\utlx-linux-arm64
  # then: $env:UTLX_BINARY = "$PWD\out\utlx-linux-arm64"; sam toolset package utlx ..\..\.. --url <platform>
#>
param(
  [string]$Version = "1.3.1",
  [string]$Engine = ""
)
$ErrorActionPreference = "Stop"

if (-not $Engine) {
  foreach ($c in @("docker", "podman")) {
    if (Get-Command $c -ErrorAction SilentlyContinue) { $Engine = $c; break }
  }
}
if (-not $Engine) { throw "need docker or podman on PATH" }

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$out = Join-Path $here "out"
New-Item -ItemType Directory -Force -Path $out | Out-Null

$script = @'
set -euo pipefail
microdnf install -y git findutils >/dev/null
git clone --depth 1 --branch "v$VERSION" https://github.com/grauwen/utl-x /src
cd /src
./gradlew --no-daemon :modules:cli:nativeCompile
cp modules/cli/build/native/nativeCompile/utlx /out/utlx-linux-arm64
'@
# This file has CRLF line endings; bash in the container needs LF.
$script = $script -replace "`r", ""

& $Engine run --rm --platform linux/arm64 `
  -v "${out}:/out" `
  -e "VERSION=$Version" `
  ghcr.io/graalvm/native-image-community:21 `
  bash -c $script
if ($LASTEXITCODE -ne 0) { throw "container build failed ($LASTEXITCODE)" }

Write-Host "built $(Join-Path $out 'utlx-linux-arm64')"
