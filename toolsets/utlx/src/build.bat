@echo off
REM Windows build script; the Mac/Linux equivalent is build.sh (same options).
REM Builds the Go tool binary, copies the manifest, and bundles the utl-x CLI
REM as bin\utlx (bin\utlx.exe for a Windows target).
REM
REM Invoked by sam config apply / sam toolset package / sam toolset validate,
REM which set SAM_TOOL_TARGET_OS / SAM_TOOL_TARGET_ARCH for the Secure Tool
REM Runtime. For a manual build set both first, e.g.
REM   set SAM_TOOL_TARGET_OS=windows& set SAM_TOOL_TARGET_ARCH=amd64& build.bat
REM
REM bin\utlx source:
REM   UTLX_BINARY=C:\path\to\utlx   use this binary (must match the target)
REM   otherwise download the utl-x release (needs curl.exe, Windows 10+):
REM     windows/amd64 -> utlx-windows-x64.exe   (SAM desktop on Windows)
REM     linux/amd64   -> utlx-linux-x64
REM     darwin/arm64  -> utlx-macos-arm64
REM   linux/arm64 has no release binary: ..\tools\build-utlx-linux-arm64.ps1
REM   UTLX_VERSION (default 1.3.1), UTLX_SHA256 (optional), UTLX_SKIP_BINARY=1
setlocal EnableExtensions
if "%SAM_TOOL_BUILD_OUT%"=="" set "SAM_TOOL_BUILD_OUT=dist"
if "%SAM_TOOL_NAME%"=="" set "SAM_TOOL_NAME=utlx"
if "%SAM_TOOL_TARGET_OS%"=="" ( echo SAM_TOOL_TARGET_OS must be set by sam config apply, or exported for a manual build ^(e.g. windows^) 1>&2 & exit /b 1 )
if "%SAM_TOOL_TARGET_ARCH%"=="" ( echo SAM_TOOL_TARGET_ARCH must be set by sam config apply, or exported for a manual build ^(e.g. amd64^) 1>&2 & exit /b 1 )
if "%UTLX_VERSION%"=="" set "UTLX_VERSION=1.3.1"
if "%UTLX_CACHE_DIR%"=="" set "UTLX_CACHE_DIR=%~dp0..\.sam-cache\utlx"

REM Windows executables need .exe; the STR resolves the manifest's
REM extensionless executable name to <name>.exe there.
set "EXE="
if /I "%SAM_TOOL_TARGET_OS%"=="windows" set "EXE=.exe"

if not exist "%SAM_TOOL_BUILD_OUT%" mkdir "%SAM_TOOL_BUILD_OUT%"

REM 1. The Go tool binary.
set "CGO_ENABLED=0"
set "GOOS=%SAM_TOOL_TARGET_OS%"
set "GOARCH=%SAM_TOOL_TARGET_ARCH%"
go build -trimpath -buildvcs=false -o "%SAM_TOOL_BUILD_OUT%\%SAM_TOOL_NAME%%EXE%" .
if errorlevel 1 exit /b %errorlevel%

REM 2. The manifest.
copy /Y manifest.yaml "%SAM_TOOL_BUILD_OUT%\manifest.yaml" >nul
if errorlevel 1 exit /b %errorlevel%

REM 3. The utl-x CLI.
if "%UTLX_SKIP_BINARY%"=="1" ( echo build.bat: UTLX_SKIP_BINARY=1, not bundling utl-x 1>&2 & exit /b 0 )

if "%UTLX_BINARY%"=="" goto :download
if not exist "%UTLX_BINARY%" goto :nobinary
set "SRC=%UTLX_BINARY%"
goto :verify

:download

set "ASSET="
if /I "%SAM_TOOL_TARGET_OS%/%SAM_TOOL_TARGET_ARCH%"=="windows/amd64" set "ASSET=utlx-windows-x64.exe"
if /I "%SAM_TOOL_TARGET_OS%/%SAM_TOOL_TARGET_ARCH%"=="linux/amd64" set "ASSET=utlx-linux-x64"
if /I "%SAM_TOOL_TARGET_OS%/%SAM_TOOL_TARGET_ARCH%"=="darwin/arm64" set "ASSET=utlx-macos-arm64"
if "%ASSET%"=="" (
  echo build.bat: utl-x %UTLX_VERSION% has no release binary for %SAM_TOOL_TARGET_OS%/%SAM_TOOL_TARGET_ARCH%. 1>&2
  echo   Build one ^(linux/arm64^): ..\tools\build-utlx-linux-arm64.ps1, then set UTLX_BINARY 1>&2
  echo   or set UTLX_SKIP_BINARY=1 and provide utlx in the STR image ^(utlx_path config^). 1>&2
  exit /b 1
)

set "SRC=%UTLX_CACHE_DIR%\%UTLX_VERSION%\%ASSET%"
if exist "%SRC%" goto :verify
if not exist "%UTLX_CACHE_DIR%\%UTLX_VERSION%" mkdir "%UTLX_CACHE_DIR%\%UTLX_VERSION%"
set "URL=https://github.com/grauwen/utl-x/releases/download/v%UTLX_VERSION%/%ASSET%"
echo build.bat: downloading %URL% 1>&2
curl.exe -fsSL --retry 3 -o "%SRC%.part" "%URL%"
if errorlevel 1 ( del /q "%SRC%.part" 2>nul & echo build.bat: download failed 1>&2 & exit /b 1 )
move /Y "%SRC%.part" "%SRC%" >nul

:verify
if "%UTLX_SHA256%"=="" goto :copy
set "ACTUAL="
for /f "usebackq delims=" %%H in (`powershell -NoProfile -Command "(Get-FileHash -Algorithm SHA256 -LiteralPath '%SRC%').Hash.ToLower()"`) do set "ACTUAL=%%H"
if /I not "%ACTUAL%"=="%UTLX_SHA256%" ( echo build.bat: utl-x checksum mismatch ^(%ACTUAL%^) 1>&2 & exit /b 1 )

:copy
if not exist "%SAM_TOOL_BUILD_OUT%\bin" mkdir "%SAM_TOOL_BUILD_OUT%\bin"
copy /Y "%SRC%" "%SAM_TOOL_BUILD_OUT%\bin\utlx%EXE%" >nul
if errorlevel 1 exit /b %errorlevel%
endlocal
exit /b 0

:nobinary
echo build.bat: UTLX_BINARY not found: "%UTLX_BINARY%" 1>&2
exit /b 1
