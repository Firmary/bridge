#!/bin/bash
# Pack the Linux binary with LICENSE and NOTICE into a flat tarball (no top-level folder, owner 0).
#
#   packaging/linux/build-tarball.sh ARCH BINARY OUTDIR      (ARCH: x64 or arm64)
#
# Writes OUTDIR/firmary-link-linux-<ARCH>.tar.gz, the archive install.sh downloads.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 ARCH BINARY OUTDIR" >&2
  exit 2
fi
arch=$1 binary=$2 outdir=$3
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

case "$arch" in x64 | arm64) ;; *) echo "bad arch: $arch (x64 or arm64)" >&2; exit 2 ;; esac
[ -f "$binary" ] || { echo "no binary at $binary" >&2; exit 2; }
mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
install -m 0755 "$binary" "$work/firmary-link"
install -m 0644 "$root/LICENSE" "$root/NOTICE" "$work/"

tarball="$outdir/firmary-link-linux-$arch.tar.gz"
tar -C "$work" --owner=0 --group=0 --numeric-owner -czf "$tarball" firmary-link LICENSE NOTICE
echo "built $tarball"
