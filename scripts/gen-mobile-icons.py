#!/usr/bin/env python3
"""Generate mobile app icon assets from internal/ui/assets/goat-logo.svg.

Outputs (all paths relative to repo root):
  - mobile/android/Shell/app/src/main/res/mipmap-{m,h,xh,xxh,xxxh}dpi/
      ic_launcher.png            (square, brand-green bg, centered white goat)
      ic_launcher_round.png      (circular alpha mask, same content)
      ic_launcher_foreground.png (adaptive icon foreground: white goat on
                                  transparent, content sized for the 66dp
                                  inner safe zone of a 108dp canvas)
  - mobile/ios/Shell/App/Assets.xcassets/AppIcon.appiconset/Icon-1024.png
      (1024x1024 RGB, no alpha — iOS marketing icon)

Run from repo root:  python3 scripts/gen-mobile-icons.py
Requires:            rsvg-convert (homebrew librsvg) + Pillow
"""
from __future__ import annotations

import io
import pathlib
import subprocess
import sys

from PIL import Image, ImageDraw

REPO = pathlib.Path(__file__).resolve().parent.parent
SVG  = REPO / "internal/ui/assets/goat-logo.svg"
BRAND_GREEN = (32, 150, 79, 255)   # #20964f
WHITE       = (255, 255, 255, 255)

# Android density buckets:
#   - legacy launcher size (square fallback)
#   - adaptive icon canvas size (foreground PNG, 108dp at this density)
ANDROID_DENSITIES = [
    ("mdpi",     48,  108),
    ("hdpi",     72,  162),
    ("xhdpi",    96,  216),
    ("xxhdpi",  144,  324),
    ("xxxhdpi", 192,  432),
]


def render_goat(fill_hex: str, target_w: int) -> Image.Image:
    """Rasterize goat-logo.svg with the given fill color at the given width.

    rsvg-convert preserves the SVG aspect ratio. SVG is 2752x1536 → ~1.79:1.
    """
    svg_text = SVG.read_text()
    # Swap the brand-green fill for the requested color in-memory.
    swapped = svg_text.replace('fill="#20964f"', f'fill="{fill_hex}"')
    proc = subprocess.run(
        ["rsvg-convert", "--format=png", f"--width={target_w}"],
        input=swapped.encode("utf-8"),
        check=True,
        capture_output=True,
    )
    return Image.open(io.BytesIO(proc.stdout)).convert("RGBA")


def square_tile(size: int, content_fraction: float = 0.70) -> Image.Image:
    """Brand-green square canvas + centered white goat sized to content_fraction.

    Returns RGBA. content_fraction is the goat width as a fraction of canvas.
    """
    canvas = Image.new("RGBA", (size, size), BRAND_GREEN)
    content_w = int(size * content_fraction)
    goat = render_goat("#ffffff", content_w)
    # Re-render at a higher source resolution then downscale to avoid jaggies.
    src_w = max(content_w * 4, 512)
    hires = render_goat("#ffffff", src_w)
    goat = hires.resize((content_w, int(content_w * hires.height / hires.width)),
                        Image.LANCZOS)
    # Center it.
    x = (size - goat.width) // 2
    y = (size - goat.height) // 2
    canvas.alpha_composite(goat, (x, y))
    return canvas


def circle_mask(size: int) -> Image.Image:
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).ellipse((0, 0, size - 1, size - 1), fill=255)
    return mask


def round_tile(size: int, content_fraction: float = 0.70) -> Image.Image:
    """square_tile with a circular alpha mask."""
    tile = square_tile(size, content_fraction)
    tile.putalpha(circle_mask(size))
    return tile


def adaptive_foreground(canvas_size: int,
                        safe_zone_fraction: float = 0.58) -> Image.Image:
    """Adaptive icon foreground: white goat on transparent.

    Canvas is 108dp at the target density. Content must fit inside the safe
    zone diameter of 66dp (≈ 0.61 of canvas). Using 0.58 leaves a tiny margin.
    """
    canvas = Image.new("RGBA", (canvas_size, canvas_size), (0, 0, 0, 0))
    content_w = int(canvas_size * safe_zone_fraction)
    src_w = max(content_w * 4, 512)
    hires = render_goat("#ffffff", src_w)
    goat = hires.resize((content_w, int(content_w * hires.height / hires.width)),
                        Image.LANCZOS)
    x = (canvas_size - goat.width) // 2
    y = (canvas_size - goat.height) // 2
    canvas.alpha_composite(goat, (x, y))
    return canvas


def write_png(img: Image.Image, path: pathlib.Path, *, rgb: bool = False) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if rgb:
        # iOS marketing icon: flatten alpha onto brand green, save as RGB.
        bg = Image.new("RGB", img.size, BRAND_GREEN[:3])
        if img.mode == "RGBA":
            bg.paste(img.convert("RGBA"), mask=img.split()[3])
        else:
            bg.paste(img)
        bg.save(path, "PNG")
    else:
        img.save(path, "PNG")
    print(f"  wrote {path.relative_to(REPO)} ({img.size[0]}x{img.size[1]})")


def main() -> int:
    if not SVG.exists():
        print(f"SVG not found: {SVG}", file=sys.stderr)
        return 1

    android_res = REPO / "mobile/android/Shell/app/src/main/res"
    print("Android:")
    for bucket, legacy, adaptive in ANDROID_DENSITIES:
        mipmap = android_res / f"mipmap-{bucket}"
        write_png(square_tile(legacy), mipmap / "ic_launcher.png")
        write_png(round_tile(legacy),  mipmap / "ic_launcher_round.png")
        write_png(adaptive_foreground(adaptive),
                  mipmap / "ic_launcher_foreground.png")

    ios_iconset = (REPO
                   / "mobile/ios/Shell/App/Assets.xcassets/AppIcon.appiconset")
    print("iOS:")
    write_png(square_tile(1024), ios_iconset / "Icon-1024.png", rgb=True)

    print("done.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
