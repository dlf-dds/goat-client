package ui

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

func TestIconForMode_SingleTunnelDelegatesToState(t *testing.T) {
	// In single-tunnel modes the combined-icon path must not run —
	// the tray shows one silhouette tinted by the outer state. We
	// verify by comparing bytes against iconForState directly.
	cases := []struct {
		name  string
		m     mode.Mode
		outer ipc.State
	}{
		{"wg-cp0-only/connected", mode.WGCP0Only, ipc.StateConnected},
		{"wg-cp0-only/disconnected", mode.WGCP0Only, ipc.StateDisconnected},
		{"netbird-only/connecting", mode.NetbirdOnly, ipc.StateConnecting},
		{"netbird-only/error", mode.NetbirdOnly, ipc.StateError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Pass a different "inner" state to prove single-tunnel modes ignore it.
			got := iconForMode(tc.m, tc.outer, ipc.StateError)
			want := iconForState(tc.outer)
			if !bytes.Equal(got, want) {
				t.Errorf("iconForMode(%s, %s, _) did not delegate to iconForState(%s)", tc.m, tc.outer, tc.outer)
			}
		})
	}
}

func TestIconForMode_CombinedRendersDifferentFromSingle(t *testing.T) {
	// Combined mode builds a two-up composition. The output must NOT
	// match the single-state icon for either leg — otherwise the
	// operator can't visually distinguish "one tunnel green" from
	// "both tunnels green" at a glance.
	single := iconForState(ipc.StateConnected)
	combo := iconForMode(mode.Combined, ipc.StateConnected, ipc.StateConnected)
	if bytes.Equal(single, combo) {
		t.Error("combined-mode icon matches single-tunnel icon; cannot distinguish at a glance")
	}
}

func TestCombinedIcon_DecodesAsPNG(t *testing.T) {
	// The 2-up composition writes a fresh PNG; verify it round-trips.
	buf := combinedIcon(ipc.StateConnected, ipc.StateConnecting)
	img, err := png.Decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("combinedIcon decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 2 || b.Dy() < 1 {
		t.Errorf("combinedIcon bounds %v are nonsensical", b)
	}
}

func TestCombinedIcon_PerLegTinting(t *testing.T) {
	// Same input on both legs should produce the same image as the
	// permutation where both legs swap states only if the states are
	// identical. Different (outer, inner) tints should not collide.
	a := combinedIcon(ipc.StateConnected, ipc.StateConnected)
	b := combinedIcon(ipc.StateError, ipc.StateError)
	if bytes.Equal(a, b) {
		t.Error("combinedIcon(connected,connected) == combinedIcon(error,error); legs aren't tinted")
	}

	// Swap order — should NOT match (left=outer, right=inner).
	mix1 := combinedIcon(ipc.StateConnected, ipc.StateError)
	mix2 := combinedIcon(ipc.StateError, ipc.StateConnected)
	if bytes.Equal(mix1, mix2) {
		t.Error("combinedIcon: leg order doesn't matter, but it should (left=outer, right=inner)")
	}
}
