package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// In combined mode the tray surfaces both tunnels via one icon that
// stacks two tinted silhouettes side-by-side (outer | inner). One
// indicator can't capture two independent states cleanly, so we render
// the outer state in the left half and the inner state in the right
// half — operators can read "left dot green, right dot amber" at a
// glance without opening the window.

// iconForMode returns the tray PNG for the given mode + per-leg states.
// In single-tunnel modes only outerState is used. In combined mode both
// states are rendered into a 2-up composition.
func iconForMode(m mode.Mode, outerState, innerState ipc.State) []byte {
	if m != mode.Combined {
		return iconForState(outerState)
	}
	return combinedIcon(outerState, innerState)
}

// combinedIcon builds a horizontally-stacked icon at runtime by decoding
// the embedded silhouette, tinting it twice (once per leg), and pasting
// side by side. Falls back to a solid swatch if PNG decode fails — the
// systray needs *something* even if asset embedding is broken.
func combinedIcon(outer, inner ipc.State) []byte {
	mask, err := png.Decode(bytes.NewReader(goatTrayPNG))
	if err != nil {
		return solidSwatch(stateRGBA(outer))
	}
	b := mask.Bounds()
	w, h := b.Dx(), b.Dy()
	canvas := image.NewRGBA(image.Rect(0, 0, w*2+2, h))
	paintTinted(canvas, image.Rect(0, 0, w, h), mask, stateRGBA(outer))
	paintTinted(canvas, image.Rect(w+2, 0, w*2+2, h), mask, stateRGBA(inner))
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return solidSwatch(stateRGBA(outer))
	}
	return buf.Bytes()
}

// paintTinted writes the mask into dst.SubImage(rect), preserving alpha
// and replacing RGB with c's RGB.
func paintTinted(dst *image.RGBA, rect image.Rectangle, mask image.Image, c color.RGBA) {
	b := mask.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			_, _, _, a := mask.At(b.Min.X+x, b.Min.Y+y).RGBA()
			a8 := uint8(a >> 8)
			dst.SetRGBA(rect.Min.X+x, rect.Min.Y+y, color.RGBA{R: c.R, G: c.G, B: c.B, A: a8})
		}
	}
}
