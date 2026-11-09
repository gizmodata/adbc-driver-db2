#!/usr/bin/env bash
# Build the c-shared ADBC driver library (libadbc_driver_db2.{so,dylib,dll})
# for the target in GOOS/GOARCH (default: this machine).
#
# The library exports only the ADBC entrypoints (Adbc*) plus the deprecated
# Db2DriverInit alias, per the ADBC shared-library requirements
# (https://adbc-drivers.org/building-drivers/shared-library-requirements.html).
# The export lists live next to the package: exports_linux.map (version
# script), exports_darwin.txt (ld64 exported-symbols list) and
# exports_windows.def.
#
# Windows: Go's linker writes its own .def file listing every //export
# function (Db2DatabaseNew, ...) unless its extldflags contain the exact
# string "-Wl,--export-all-symbols" (cmd/link/internal/ld/lib.go). The "-L"
# in front makes the C compiler driver read that string as a library search
# directory, so the linker never sees it: GNU ld and LLVM lld (whose
# --export-all-symbols would override everything) then export only what the
# C code marks dllexport (the Adbc* wrappers) plus exports_windows.def (the
# Go-exported entrypoints). go run ./scripts/checkexports verifies the
# result; it fails the build if Go ever changes this.
#
# Usage: scripts/build_lib.sh [output path]
# CC, CGO_CFLAGS and the like pass through to go build.
set -euo pipefail
cd "$(dirname "$0")/.."

goos=$(go env GOOS)
case "$goos" in
  linux)   ext=so ;;
  darwin)  ext=dylib ;;
  windows) ext=dll ;;
  *) echo "build_lib.sh: unsupported GOOS $goos" >&2; exit 1 ;;
esac
out=${1:-pkg/db2/libadbc_driver_db2.$ext}

# The external linker runs in a temporary directory: give it absolute paths,
# in the native form on Windows (D:/a/..., not Git Bash's /d/a/...).
dir=$PWD/pkg/db2
if command -v cygpath > /dev/null 2>&1; then
  dir=$(cygpath -m "$dir")
fi
case "$goos" in
  linux)   extld="-Wl,--version-script=$dir/exports_linux.map" ;;
  darwin)  extld="-Wl,-exported_symbols_list,$dir/exports_darwin.txt" ;;
  windows) extld="-L -Wl,--export-all-symbols $dir/exports_windows.def" ;;
esac

# macOS: target the oldest release the Go toolchain supports (Go 1.27: 13.0),
# which the wheel's tag then names, not the build machine's own release. The
# flag goes into CGO_CFLAGS/CGO_LDFLAGS as well as MACOSX_DEPLOYMENT_TARGET
# because Go's build cache ignores the latter and would reuse objects built
# for another floor. Setting CGO_CFLAGS replaces cgo's "-O2 -g" default, so
# keep it.
if [ "$goos" = darwin ]; then
  export MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-13.0}
  export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
  export CGO_LDFLAGS="${CGO_LDFLAGS:--O2 -g} -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
fi

CGO_ENABLED=1 go build -buildmode=c-shared -tags driverlib \
  -ldflags "-extldflags '$extld'" -o "$out" ./pkg/db2
echo "built $out"
