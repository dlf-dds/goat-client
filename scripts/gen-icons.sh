#!/usr/bin/env bash
# Regenerate all rasterised goat-client icon assets from the SVG source.
#
# Inputs:
#   internal/ui/assets/goat-logo.svg     (single-fill #20964f silhouette)
#
# Outputs:
#   internal/ui/assets/goat-tray.png     (22x22 letterboxed, used by tray)
#   internal/ui/assets/goat-client.png   (256x256, Fyne app icon + Linux hicolor)
#   internal/ui/assets/goat-client.ico   (multi-res Windows icon)
#   internal/ui/assets/goat-client.icns  (multi-res macOS icon)
#
# Requires: rsvg-convert (Cairo), python3 with Pillow, iconutil (macOS).
set -euo pipefail

cd "$(dirname "$0")/.."

SVG=internal/ui/assets/goat-logo.svg
OUT=internal/ui/assets
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if [[ ! -f "$SVG" ]]; then
  echo "missing source: $SVG" >&2
  exit 1
fi

# Render native-aspect (1.79:1) at a generous height, then letterbox to squares
# in Python so we keep one rendering pipeline and a single padding rule.
rsvg-convert -h 1024 -b transparent "$SVG" -o "$TMP/native-1024.png"

python3 - "$TMP/native-1024.png" "$OUT" <<'PY'
import io, struct, sys
from pathlib import Path
from PIL import Image

src = Image.open(sys.argv[1]).convert("RGBA")
out = Path(sys.argv[2])

# Auto-trim transparent borders — the SVG viewBox has dead space around the
# silhouette, which would otherwise letterbox the head down to ~12 px tall
# inside a 22-px tray square. After trim the content fills its bounds.
bbox = src.getbbox()
if bbox:
    src = src.crop(bbox)

def square(size: int, padding: float = 0.05) -> Image.Image:
    # Fit src into a transparent size×size canvas, preserving aspect ratio,
    # with a small inner padding so horn tips don't kiss the edge.
    target = max(1, int(round(size * (1 - 2 * padding))))
    w, h = src.size
    scale = target / max(w, h)
    new_w = max(1, int(round(w * scale)))
    new_h = max(1, int(round(h * scale)))
    scaled = src.resize((new_w, new_h), Image.LANCZOS)
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    canvas.paste(scaled, ((size - new_w) // 2, (size - new_h) // 2), scaled)
    return canvas

# Tray micro: 22 px matches the macOS template-icon target the existing
# tray code rendered the dot at. Keeps Windows/Linux trays happy too.
square(22, padding=0.0).save(out / "goat-tray.png", optimize=True)

# 256x256 — Linux hicolor + Fyne app icon source. Filename matches what
# packaging/{deb,rpm}/nfpm.yaml already expect at this path.
square(256).save(out / "goat-client.png", optimize=True)

# Windows .ico — multi-res. Encode by hand because PIL's append_images path
# silently drops extras for ICO, producing a single-entry file.
ico_sizes = [16, 32, 48, 64, 128, 256]
entries = []
for s in ico_sizes:
    buf = io.BytesIO()
    square(s, padding=0.0 if s <= 32 else 0.05).save(buf, format="PNG", optimize=True)
    entries.append((s, buf.getvalue()))

# ICONDIR (6 bytes) + ICONDIRENTRY * N (16 bytes each), then payloads.
ico = io.BytesIO()
ico.write(struct.pack("<HHH", 0, 1, len(entries)))
data_offset = 6 + 16 * len(entries)
for s, data in entries:
    w = 0 if s >= 256 else s  # 0 == 256 per ICO spec
    h = 0 if s >= 256 else s
    ico.write(struct.pack(
        "<BBBBHHII",
        w, h, 0, 0,        # width, height, palette colors, reserved
        1, 32,             # planes, bpp
        len(data), data_offset,
    ))
    data_offset += len(data)
for _, data in entries:
    ico.write(data)
(out / "goat-client.ico").write_bytes(ico.getvalue())
PY

# macOS .icns — iconutil needs an .iconset directory with the canonical names.
ICONSET="$TMP/goat-client.iconset"
mkdir -p "$ICONSET"
python3 - "$TMP/native-1024.png" "$ICONSET" <<'PY'
import sys
from pathlib import Path
from PIL import Image

src = Image.open(sys.argv[1]).convert("RGBA")
bbox = src.getbbox()
if bbox:
    src = src.crop(bbox)
ic = Path(sys.argv[2])

def square(size: int) -> Image.Image:
    target = max(1, int(round(size * 0.90)))
    w, h = src.size
    scale = target / max(w, h)
    new_w = max(1, int(round(w * scale)))
    new_h = max(1, int(round(h * scale)))
    scaled = src.resize((new_w, new_h), Image.LANCZOS)
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    canvas.paste(scaled, ((size - new_w) // 2, (size - new_h) // 2), scaled)
    return canvas

# iconutil expects this exact set of filenames + sizes.
spec = [
    (16,  "icon_16x16.png"),
    (32,  "icon_16x16@2x.png"),
    (32,  "icon_32x32.png"),
    (64,  "icon_32x32@2x.png"),
    (128, "icon_128x128.png"),
    (256, "icon_128x128@2x.png"),
    (256, "icon_256x256.png"),
    (512, "icon_256x256@2x.png"),
    (512, "icon_512x512.png"),
    (1024,"icon_512x512@2x.png"),
]
for size, name in spec:
    square(size).save(ic / name, optimize=True)
PY

iconutil -c icns -o "$OUT/goat-client.icns" "$ICONSET"

echo "wrote:"
ls -la "$OUT"/goat-tray.png "$OUT"/goat-client.png "$OUT"/goat-client.ico "$OUT"/goat-client.icns
