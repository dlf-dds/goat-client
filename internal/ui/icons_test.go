package ui

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

func TestStateRGBA_LogoGreenForConnected(t *testing.T) {
	got := stateRGBA(ipc.StateConnected)
	want := color.RGBA{R: 0x20, G: 0x96, B: 0x4f, A: 0xff}
	if got != want {
		t.Errorf("Connected RGBA = %#v, want logo green %#v", got, want)
	}
}

func TestStateRGBA_DistinctStatesDistinctColors(t *testing.T) {
	// The tray icon + the dot indicator both look up state via this
	// function, so the four states must produce four distinct colours
	// (otherwise the operator can't tell connecting from connected at
	// a glance — the bug F-108 partially surfaced).
	colors := map[ipc.State]color.RGBA{
		ipc.StateDisconnected: stateRGBA(ipc.StateDisconnected),
		ipc.StateConnecting:   stateRGBA(ipc.StateConnecting),
		ipc.StateConnected:    stateRGBA(ipc.StateConnected),
		ipc.StateError:        stateRGBA(ipc.StateError),
	}
	seen := map[color.RGBA]ipc.State{}
	for s, c := range colors {
		if c.A != 0xff {
			t.Errorf("state %q RGBA.A = %d, want fully opaque (0xff)", s, c.A)
		}
		if other, dup := seen[c]; dup {
			t.Errorf("state %q and %q share RGBA %#v", s, other, c)
		}
		seen[c] = s
	}
}

func TestStateColor_AgreesWithStateRGBA(t *testing.T) {
	// stateColor returns color.Color (interface); stateRGBA returns
	// color.RGBA. The indicator dot uses stateColor; the tray uses
	// stateRGBA. They must agree so the two surfaces don't drift.
	for _, s := range []ipc.State{
		ipc.StateDisconnected, ipc.StateConnecting,
		ipc.StateConnected, ipc.StateError,
	} {
		r1, g1, b1, a1 := stateColor(s).RGBA()
		want := stateRGBA(s)
		r2, g2, b2, a2 := want.RGBA()
		if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
			t.Errorf("state %q: stateColor=%v, stateRGBA=%v — drifted", s, []uint32{r1, g1, b1, a1}, []uint32{r2, g2, b2, a2})
		}
	}
}

func TestIconForState_NonEmptyPNGPerState(t *testing.T) {
	// init() in icons.go populates the four state icons by tinting the
	// embedded silhouette. Each must be a non-empty PNG byte slice so
	// the systray library has something to render.
	for _, s := range []ipc.State{
		ipc.StateDisconnected, ipc.StateConnecting,
		ipc.StateConnected, ipc.StateError,
	} {
		t.Run(string(s), func(t *testing.T) {
			got := iconForState(s)
			if len(got) == 0 {
				t.Fatalf("iconForState(%q) returned empty bytes", s)
			}
			if _, err := png.Decode(bytes.NewReader(got)); err != nil {
				t.Errorf("iconForState(%q) is not a decodable PNG: %v", s, err)
			}
		})
	}
}

func TestIconForState_DefaultDisconnected(t *testing.T) {
	// Unknown states must fall back to disconnected (gray) so a
	// malformed daemon reply doesn't crash the tray.
	want := iconForState(ipc.StateDisconnected)
	got := iconForState(ipc.State("nonsense"))
	if !bytes.Equal(got, want) {
		t.Errorf("iconForState(unknown) did not fall back to Disconnected: len(got)=%d len(want)=%d", len(got), len(want))
	}
}

func TestSolidSwatch_DecodesToRGBA(t *testing.T) {
	// solidSwatch is the last-resort fallback; even there the bytes
	// must round-trip through png.Decode so the tray library accepts
	// them.
	for _, c := range []color.RGBA{
		{0xff, 0x00, 0x00, 0xff},
		{0x00, 0xff, 0x00, 0xff},
		{0x00, 0x00, 0xff, 0xff},
	} {
		buf := solidSwatch(c)
		img, err := png.Decode(bytes.NewReader(buf))
		if err != nil {
			t.Fatalf("solidSwatch(%#v) decode: %v", c, err)
		}
		r, g, b, a := img.At(0, 0).RGBA()
		// PNG decoder returns 16-bit values; mask back to 8 bits.
		gotR, gotG, gotB, gotA := uint8(r>>8), uint8(g>>8), uint8(b>>8), uint8(a>>8)
		if gotR != c.R || gotG != c.G || gotB != c.B || gotA != c.A {
			t.Errorf("solidSwatch(%#v) → pixel %v %v %v %v", c, gotR, gotG, gotB, gotA)
		}
	}
}
