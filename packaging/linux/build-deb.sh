#!/bin/bash
# Build the Firmary Bridge .deb for one architecture. The package, command and file names keep their Firmary Link
# names (firmary-link), so installing it over an older one upgrades it.
#
#   packaging/linux/build-deb.sh VERSION ARCH BINARY OUTDIR      (ARCH: amd64 or arm64)
#
# Writes OUTDIR/firmary-link_<VERSION>_<ARCH>.deb. Needs dpkg-deb only.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 VERSION ARCH BINARY OUTDIR" >&2
  exit 2
fi
version=$1 arch=$2 binary=$3 outdir=$4
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "bad version: $version" >&2; exit 2; }
case "$arch" in amd64 | arm64) ;; *) echo "bad arch: $arch (amd64 or arm64)" >&2; exit 2 ;; esac
[ -f "$binary" ] || { echo "no binary at $binary" >&2; exit 2; }
mkdir -p "$outdir"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
pkg="$work/pkg"
doc="$pkg/usr/share/doc/firmary-link"

install -d -m 0755 "$pkg/DEBIAN" "$pkg/usr/bin" "$pkg/usr/share/applications" \
  "$pkg/usr/share/icons/hicolor/256x256/apps" "$doc"
install -m 0755 "$binary" "$pkg/usr/bin/firmary-link"
install -m 0644 "$here/firmary-link.desktop" "$pkg/usr/share/applications/firmary-link.desktop"
install -m 0644 "$root/assets/linux/firmary-link.png" "$pkg/usr/share/icons/hicolor/256x256/apps/firmary-link.png"

{
  cat <<'COPYRIGHT'
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: firmary-link
Upstream-Contact: Firmary <admin@firmary.app>
Source: https://github.com/Firmary/bridge

Files: *
Copyright: 2026 Firmary
License: Apache-2.0

License: Apache-2.0
COPYRIGHT
  # The NOTICE, indented as a continuation of the license field.
  sed -e 's/^$/./' -e 's/^/ /' "$root/NOTICE"
  echo " ."
  echo " On Debian systems, the full text of the Apache License, Version 2.0 can be"
  echo " found in /usr/share/common-licenses/Apache-2.0."
} > "$doc/copyright"
chmod 0644 "$doc/copyright"

# A pure-Go (CGO_ENABLED=0) build is static by design.
install -d -m 0755 "$pkg/usr/share/lintian/overrides"
echo 'firmary-link: statically-linked-binary [usr/bin/firmary-link]' > "$pkg/usr/share/lintian/overrides/firmary-link"
chmod 0644 "$pkg/usr/share/lintian/overrides/firmary-link"

printf 'firmary-link (%s) stable; urgency=medium\n\n  * Firmary Bridge %s.\n\n -- Firmary <admin@firmary.app>  %s\n' \
  "$version" "$version" "$(date -u -R)" | gzip -9n > "$doc/changelog.gz"
chmod 0644 "$doc/changelog.gz"

size=$(du -sk --exclude=DEBIAN "$pkg" | cut -f1)
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: firmary-link
Version: $version
Architecture: $arch
Maintainer: Firmary <admin@firmary.app>
Installed-Size: $size
Section: net
Priority: optional
Homepage: https://github.com/Firmary/bridge
Description: connect your own Claude or ChatGPT plan to Firmary
 Firmary Bridge runs on your computer or server and connects the Claude or
 ChatGPT plan you already have to Firmary. It sits in the tray on a desktop
 and runs headless on a server.
CONTROL
chmod 0644 "$pkg/DEBIAN/control"

deb="$outdir/firmary-link_${version}_${arch}.deb"
# xz rather than dpkg's newer zstd default, so older Debian and Ubuntu releases can read it.
dpkg-deb -Zxz --root-owner-group --build "$pkg" "$deb"
echo "built $deb"
