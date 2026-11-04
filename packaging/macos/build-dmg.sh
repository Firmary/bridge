#!/bin/bash
# Build "Firmary Link.app" around a universal macOS binary, ad-hoc sign it and pack it into Firmary-Link.dmg.
#
#   packaging/macos/build-dmg.sh VERSION UNIVERSAL_BINARY OUTDIR
#
# Runs on macOS only (codesign, hdiutil). The beta is unsigned: ad-hoc signatures, no Developer ID, no notarization.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 VERSION UNIVERSAL_BINARY OUTDIR" >&2
  exit 2
fi
version=$1 binary=$2 outdir=$3
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "bad version: $version" >&2; exit 2; }
[ -f "$binary" ] || { echo "no binary at $binary" >&2; exit 2; }
mkdir -p "$outdir"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

app="$work/dmg/Firmary Link.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
sed "s/@VERSION@/$version/g" "$here/Info.plist.in" > "$app/Contents/Info.plist"
plutil -lint "$app/Contents/Info.plist"
printf 'APPL????' > "$app/Contents/PkgInfo"
cp "$root/assets/macos/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
cp "$binary" "$app/Contents/MacOS/firmary-link"
chmod 0755 "$app/Contents/MacOS/firmary-link"

# Ad-hoc sign the executable, then the bundle (which seals Info.plist and Resources into the signature).
codesign --force --sign - --identifier app.firmary.link "$app/Contents/MacOS/firmary-link"
codesign --force --sign - "$app"
codesign --verify --strict --verbose=2 "$app"

ln -s /Applications "$work/dmg/Applications"

dmg="$outdir/Firmary-Link.dmg"
rm -f "$dmg"
# hdiutil occasionally fails with "Resource busy" on hosted runners; a short retry clears it.
for attempt in 1 2 3; do
  if hdiutil create -volname "Firmary Link" -srcfolder "$work/dmg" -fs HFS+ -format UDZO -ov "$dmg"; then
    break
  fi
  [ "$attempt" -lt 3 ] || exit 1
  sleep 5
done
hdiutil verify "$dmg"
echo "built $dmg"
