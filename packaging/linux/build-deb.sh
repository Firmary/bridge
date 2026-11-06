#!/bin/bash
# Build the Firmary Bridge .deb for one architecture. The package is firmary-bridge (0.3.5 on); it replaces the
# firmary-link package of 0.2.0 to 0.3.4, so installing it over one of those upgrades it. The files it installs keep
# their Firmary Link names (the firmary-link command, desktop entry and icon): an installed copy, its service and its
# open-at-login entry find them there.
#
#   packaging/linux/build-deb.sh VERSION ARCH BINARY OUTDIR      (ARCH: amd64 or arm64)
#
# Writes OUTDIR/firmary-bridge_<VERSION>_<ARCH>.deb. Needs dpkg-deb only.
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
package=firmary-bridge
doc="$pkg/usr/share/doc/$package"

install -d -m 0755 "$pkg/DEBIAN" "$pkg/usr/bin" "$pkg/usr/share/applications" \
  "$pkg/usr/share/icons/hicolor/256x256/apps" "$doc"
install -m 0755 "$binary" "$pkg/usr/bin/firmary-link"
install -m 0644 "$here/firmary-link.desktop" "$pkg/usr/share/applications/firmary-link.desktop"
install -m 0644 "$root/assets/linux/firmary-link.png" "$pkg/usr/share/icons/hicolor/256x256/apps/firmary-link.png"

{
  cat <<'COPYRIGHT'
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: firmary-bridge
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
echo "$package: statically-linked-binary [usr/bin/firmary-link]" > "$pkg/usr/share/lintian/overrides/$package"
chmod 0644 "$pkg/usr/share/lintian/overrides/$package"

printf '%s (%s) stable; urgency=medium\n\n  * Firmary Bridge %s.\n\n -- Firmary <admin@firmary.app>  %s\n' \
  "$package" "$version" "$version" "$(date -u -R)" | gzip -9n > "$doc/changelog.gz"
chmod 0644 "$doc/changelog.gz"

size=$(du -sk --exclude=DEBIAN "$pkg" | cut -f1)
# Conflicts, Replaces and Provides firmary-link: installing this removes the old package and takes over its files,
# and anything that asked for firmary-link is still satisfied (Debian Policy 7.6.2, replacing whole packages).
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: $package
Version: $version
Architecture: $arch
Conflicts: firmary-link
Replaces: firmary-link
Provides: firmary-link
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

deb="$outdir/${package}_${version}_${arch}.deb"
# xz rather than dpkg's newer zstd default, so older Debian and Ubuntu releases can read it.
dpkg-deb -Zxz --root-owner-group --build "$pkg" "$deb"
echo "built $deb"
