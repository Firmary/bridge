#!/usr/bin/env python3
"""Generate every Firmary Link icon from the Firmary mark.

The mark is a 512x512 rounded square (rx 96, #0f172a) with a white "F":
  M165 387V125H347V171.4H219V235.8H338.4V280.9H219V387Z

Run from the repository root:  python3 -I scripts/icons.py
Needs Pillow. The output is committed; rerun only when the mark changes.
"""

import os
import sys

from PIL import Image, ImageDraw

NAVY = (0x0F, 0x17, 0x2A, 255)
WHITE = (255, 255, 255, 255)
F = [(165, 387), (165, 125), (347, 125), (347, 171.4), (219, 171.4), (219, 235.8),
     (338.4, 235.8), (338.4, 280.9), (219, 280.9), (219, 387)]
SS = 8  # supersampling factor; every icon is drawn large and scaled down


def mark(size, inset=0.0):
    """The full-colour mark at size x size. inset is the transparent margin as a fraction of size."""
    big = size * SS
    img = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    off = big * inset
    scale = (big - 2 * off) / 512
    d.rounded_rectangle([off, off, big - off - 1, big - off - 1], radius=96 * scale, fill=NAVY)
    d.polygon([(off + x * scale, off + y * scale) for x, y in F], fill=WHITE)
    return img.resize((size, size), Image.LANCZOS)


def glyph(size, height_frac):
    """The "F" alone, black on transparent (alpha only), centred, its height height_frac of size."""
    big = size * SS
    alpha = Image.new("L", (big, big), 0)
    d = ImageDraw.Draw(alpha)
    min_x, max_x, min_y, max_y = 165, 347, 125, 387
    scale = big * height_frac / (max_y - min_y)
    ox = (big - (max_x - min_x) * scale) / 2 - min_x * scale
    oy = (big - (max_y - min_y) * scale) / 2 - min_y * scale
    d.polygon([(ox + x * scale, oy + y * scale) for x, y in F], fill=255)
    alpha = alpha.resize((size, size), Image.LANCZOS)
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    img.putalpha(alpha)
    return img


def save_ico(path, sizes):
    imgs = [mark(s) for s in sizes]
    largest = imgs[-1]
    largest.save(path, format="ICO", sizes=[(s, s) for s in sizes], append_images=imgs[:-1])


def main():
    root = os.getcwd()
    if not os.path.isfile(os.path.join(root, "go.mod")):
        sys.exit("run from the repository root")

    def out(*parts):
        p = os.path.join(root, *parts)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        return p

    mark(1024).save(out("assets", "icon-1024.png"), optimize=True)

    # macOS app icons sit on Apple's grid: the 824pt rounded square inside a 1024pt canvas.
    icns_sizes = [16, 32, 64, 128, 256, 512, 1024]
    icns = [mark(s, inset=100 / 1024) for s in icns_sizes]
    icns[-1].save(out("assets", "macos", "AppIcon.icns"), format="ICNS", append_images=icns[:-1])

    save_ico(out("assets", "windows", "firmary-link.ico"), [16, 24, 32, 48, 64, 128, 256])
    mark(256).save(out("assets", "linux", "firmary-link.png"), optimize=True)

    # Tray icons the Go code embeds.
    glyph(44, 32 / 44).save(out("internal", "tray", "icons", "tray-template.png"), optimize=True)
    mark(64).save(out("internal", "tray", "icons", "tray.png"), optimize=True)
    save_ico(out("internal", "tray", "icons", "tray.ico"), [16, 32, 48])


if __name__ == "__main__":
    main()
