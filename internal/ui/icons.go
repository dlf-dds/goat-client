package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	_ "embed"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// Tray icons are the goat-client ram-head silhouette tinted by state.
// One source PNG (the silhouette with its alpha channel intact) is embedded
// and at init time we produce one tinted variant per state — mirrors the
// netbird upstream pattern of swapping the tray icon on state change, but
// with a single source asset rather than per-state hand-edited PNGs.
//
// "Connected" uses the logo's own green (#20964f) so the healthy state
// matches the brand mark literally; transitional / error / idle states
// keep the standard semantic palette (amber / red / gray).

//go:embed assets/goat-tray.png
var goatTrayPNG []byte

//go:embed assets/goat-client.png
var goatAppIconPNG []byte

var (
	iconDisconnected []byte
	iconConnecting   []byte
	iconConnected    []byte
	iconError        []byte
)

func init() {
	mask, err := png.Decode(bytes.NewReader(goatTrayPNG))
	if err != nil {
		iconDisconnected = solidSwatch(stateRGBA(ipc.StateDisconnected))
		iconConnecting = solidSwatch(stateRGBA(ipc.StateConnecting))
		iconConnected = solidSwatch(stateRGBA(ipc.StateConnected))
		iconError = solidSwatch(stateRGBA(ipc.StateError))
		return
	}
	iconDisconnected = tintMask(mask, stateRGBA(ipc.StateDisconnected))
	iconConnecting = tintMask(mask, stateRGBA(ipc.StateConnecting))
	iconConnected = tintMask(mask, stateRGBA(ipc.StateConnected))
	iconError = tintMask(mask, stateRGBA(ipc.StateError))
}

// iconForState picks the tray icon byte buffer matching `state`.
func iconForState(state ipc.State) []byte {
	switch state {
	case ipc.StateConnected:
		return iconConnected
	case ipc.StateConnecting:
		return iconConnecting
	case ipc.StateError:
		return iconError
	default:
		return iconDisconnected
	}
}

// stateRGBA returns the tray-tint RGBA for `s`. Kept in sync with
// stateColor() in window.go so the indicator dot and the tray icon
// agree on colour.
func stateRGBA(s ipc.State) color.RGBA {
	switch s {
	case ipc.StateConnected:
		return color.RGBA{R: 0x20, G: 0x96, B: 0x4f, A: 0xff} // logo green
	case ipc.StateConnecting:
		return color.RGBA{R: 0xe6, G: 0x9f, B: 0x00, A: 0xff}
	case ipc.StateError:
		return color.RGBA{R: 0xc0, G: 0x39, B: 0x2b, A: 0xff}
	default:
		return color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
	}
}

// tintMask returns a PNG of `mask` recoloured so every pixel's RGB is
// replaced by c's RGB while the source alpha is preserved — silhouette
// in the requested colour with clean antialiased edges.
func tintMask(mask image.Image, c color.RGBA) []byte {
	b := mask.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := mask.At(x, y).RGBA()
			a8 := uint8(a >> 8)
			out.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: a8})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return solidSwatch(c)
	}
	return buf.Bytes()
}

// solidSwatch produces a 1x1 PNG of c as a last-resort fallback if the
// embedded silhouette fails to decode — every systray library accepts a
// non-empty PNG and surfaces *something* rather than no icon at all.
func solidSwatch(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
