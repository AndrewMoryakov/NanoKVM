#!/bin/bash
# Cross-compiles the NetBird client for riscv64 and packs it the way the
# on-device installer expects.
#
# NetBird publishes no official riscv64 build, so NanoKVM builds one and ships
# it as a release asset. The device downloads it on demand, the same way it
# downloads Tailscale from the vendor. Nothing is bundled into the OTA package:
# the client is ~38 MB and most devices never enable it.
#
# The version is pinned by kvmapp/system/netbird/VERSION — the single source of
# truth for what the firmware expects and what this script builds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
VERSION_FILE="$ROOT/kvmapp/system/netbird/VERSION"
OUT_DIR="${1:-$ROOT/build/release}"

if [ ! -f "$VERSION_FILE" ]; then
  echo "[ERROR] missing $VERSION_FILE" >&2
  exit 1
fi

VERSION="$(tr -d '[:space:]' < "$VERSION_FILE")"
if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "[ERROR] invalid NetBird version '$VERSION', expected MAJOR.MINOR.PATCH" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "[INFO] building NetBird v$VERSION for riscv64"
git clone --depth 1 --branch "v$VERSION" https://github.com/netbirdio/netbird "$WORK/netbird"

STAGE="$WORK/netbird_${VERSION}_riscv64"
mkdir -p "$STAGE"

(
  cd "$WORK/netbird"
  # -trimpath removes the (temporary, per-run) build directory from the binary;
  # without it two builds of the same tag differ byte for byte.
  CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 go build \
    -trimpath \
    -ldflags "-s -w -X github.com/netbirdio/netbird/version.version=$VERSION" \
    -o "$STAGE/netbird" \
    ./client
)

# Carried inside the archive so the device records the version it actually
# installed, instead of trusting what the firmware expected.
printf '%s\n' "$VERSION" > "$STAGE/VERSION"

# Same check scripts/package.sh uses for every shipped binary: coreutils only,
# no binutils dependency. ELF magic, then e_machine == 243 (riscv64).
MAGIC="$(od -An -tx1 -N4 "$STAGE/netbird" | tr -d ' \n')"
MACHINE="$(od -An -tu1 -j18 -N1 "$STAGE/netbird" | tr -d ' \n')"
if [ "$MAGIC" != "7f454c46" ] || [ "$MACHINE" != "243" ]; then
  echo "[ERROR] built binary is not a riscv64 ELF (e_machine=$MACHINE)" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
TARBALL="$OUT_DIR/netbird_riscv64.tgz"
# Reproducible, like scripts/package.sh does for the main tarball: the published
# asset must be byte-comparable with a local rebuild.
tar --sort=name --owner=0 --group=0 --numeric-owner \
    --mtime="@0" \
    -cf - -C "$WORK" "netbird_${VERSION}_riscv64" | gzip -n -9 > "$TARBALL"

echo "[INFO] wrote $TARBALL ($(du -h "$TARBALL" | cut -f1))"
