package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

func TestOrDash_EmptyReturnsDash(t *testing.T) {
	if got := orDash(""); got != "—" {
		t.Errorf("orDash(\"\") = %q, want %q", got, "—")
	}
	if got := orDash("hello"); got != "hello" {
		t.Errorf("orDash(non-empty) drift: %q", got)
	}
}

func TestFormatHandshake_Zero(t *testing.T) {
	if got := formatHandshake(time.Time{}); got != "never" {
		t.Errorf("formatHandshake(zero) = %q, want %q", got, "never")
	}
}

func TestFormatHandshake_NonZeroIncludesTimestamp(t *testing.T) {
	ts := time.Date(2026, 5, 14, 8, 0, 0, 0, time.UTC)
	got := formatHandshake(ts)
	if !strings.Contains(got, "2026-05-14T08:00:00Z") {
		t.Errorf("formatHandshake(%v) = %q, want RFC3339 timestamp", ts, got)
	}
	if !strings.Contains(got, "ago)") {
		t.Errorf("formatHandshake should include duration suffix; got %q", got)
	}
}

func TestStatusPane_WGCP0OnlyMode(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{
		Mode:          "wg-cp0-only",
		State:         ipc.StateConnected,
		InterfaceName: "wg-cp0",
		LastHandshake: stableTime(),
		BytesIn:       1024,
		BytesOut:      2048,
		PeerPubKey:    "test-peer-pubkey-base64==",
		Endpoints:     []string{"203.0.113.5:51820"},
	}

	p := newStatusPane(fc)

	if !strings.Contains(p.header.Text, "wg-cp0 only") {
		t.Errorf("header.Text = %q, want wg-cp0 only display", p.header.Text)
	}
	// Stack must contain only the wg card.
	if got := len(p.stack.Objects); got != 1 {
		t.Errorf("stack.Objects len = %d, want 1 (wg card only)", got)
	}
	if p.wg.state.Text != "Connected" {
		t.Errorf("wg.state = %q, want Connected", p.wg.state.Text)
	}
	if p.wg.bytesIn.Text != "1024" || p.wg.bytesOut.Text != "2048" {
		t.Errorf("wg bytes in/out = %q/%q, want 1024/2048", p.wg.bytesIn.Text, p.wg.bytesOut.Text)
	}
	if !strings.Contains(p.wg.endpoints.Text, "203.0.113.5:51820") {
		t.Errorf("wg.endpoints = %q, missing test endpoint", p.wg.endpoints.Text)
	}
}

func TestStatusPane_NetbirdOnlyMode(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{
		Mode: "netbird-only",
		InnerMesh: &ipc.InnerMeshInfo{
			State:         ipc.StateConnected,
			PeerCount:     5,
			BytesIn:       4096,
			BytesOut:      1024,
			LastHandshake: stableTime(),
		},
	}

	p := newStatusPane(fc)

	if !strings.Contains(p.header.Text, "netbird only") {
		t.Errorf("header.Text = %q, want netbird only display", p.header.Text)
	}
	if got := len(p.stack.Objects); got != 1 {
		t.Errorf("stack.Objects len = %d, want 1 (mesh card only)", got)
	}
	if p.mesh.state.Text != "Connected" {
		t.Errorf("mesh.state = %q, want Connected", p.mesh.state.Text)
	}
	if p.mesh.peerCount.Text != "5" {
		t.Errorf("mesh.peerCount = %q, want 5", p.mesh.peerCount.Text)
	}
}

func TestStatusPane_CombinedMode(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{
		Mode:          "combined",
		State:         ipc.StateConnected,
		InterfaceName: "wg-cp0",
		PeerPubKey:    "outer-peer-base64==",
		InnerMesh: &ipc.InnerMeshInfo{
			State:     ipc.StateConnecting,
			PeerCount: 3,
		},
	}

	p := newStatusPane(fc)

	if got := len(p.stack.Objects); got != 2 {
		t.Errorf("stack.Objects len = %d, want 2 (both cards)", got)
	}
	if p.wg.state.Text != "Connected" {
		t.Errorf("wg.state = %q, want Connected", p.wg.state.Text)
	}
	if p.mesh.state.Text != "Connecting..." {
		t.Errorf("mesh.state = %q, want Connecting...", p.mesh.state.Text)
	}
}

func TestStatusPane_CombinedMode_SelectableBadge(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{Mode: "combined", InnerMesh: &ipc.InnerMeshInfo{}}

	p := newStatusPane(fc)

	// In combined mode the selection badge is visible.
	if p.wg.badge.Text == "" {
		t.Error("wg.badge.Text empty in combined mode; selection badge should show")
	}
	// Default selection is wg-cp0.
	if p.selected != "wg-cp0" {
		t.Errorf("default selected = %q, want wg-cp0", p.selected)
	}
}

func TestStatusPane_StatusError_ShowsErrorMessage(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.statusErr = errBoom

	p := newStatusPane(fc)

	if !strings.Contains(p.errorMessage.Text, "Failed to fetch status") {
		t.Errorf("errorMessage.Text = %q, want fetch-error message", p.errorMessage.Text)
	}
}

func TestStatusPane_NoInnerMesh_DefaultsToDisconnected(t *testing.T) {
	// Combined mode with no inner-mesh snapshot from the daemon (e.g.
	// during a mode-switch transition) must not panic and must show
	// Disconnected on the mesh card.
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{
		Mode:  "combined",
		State: ipc.StateConnected,
	}

	p := newStatusPane(fc)

	if p.mesh.state.Text != "Disconnected" {
		t.Errorf("mesh.state with nil InnerMesh = %q, want Disconnected", p.mesh.state.Text)
	}
	if p.mesh.peerCount.Text != "0" {
		t.Errorf("mesh.peerCount with nil InnerMesh = %q, want 0", p.mesh.peerCount.Text)
	}
}

func TestStatusPane_InvalidModeFallsBackToWGCP0(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{Mode: "bogus-mode", State: ipc.StateConnected}

	p := newStatusPane(fc)

	// Bogus mode → wg-cp0-only.
	if got := len(p.stack.Objects); got != 1 {
		t.Errorf("stack.Objects len = %d, want 1 (fallback wg-cp0-only)", got)
	}
	if !strings.Contains(p.header.Text, "wg-cp0 only") {
		t.Errorf("header.Text = %q, want wg-cp0 only fallback", p.header.Text)
	}
}

func TestStatusPane_EmptyEndpointsShowsDash(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{Mode: "wg-cp0-only", State: ipc.StateConnected, Endpoints: nil}

	p := newStatusPane(fc)

	if p.wg.endpoints.Text != "—" {
		t.Errorf("empty endpoints = %q, want em-dash", p.wg.endpoints.Text)
	}
}

func TestTunnelCard_SetSelected_OnlyVisibleWhenSelectable(t *testing.T) {
	test.NewTempApp(t)
	c := newTunnelCard("test", nil)

	c.SetSelectable(false)
	c.SetSelected(true)
	if c.badge.Text != "" {
		t.Errorf("badge.Text on non-selectable card = %q, want empty", c.badge.Text)
	}

	c.SetSelectable(true)
	c.SetSelected(true)
	if !strings.Contains(c.badge.Text, "selected") {
		t.Errorf("badge.Text on selected = %q, want 'selected'", c.badge.Text)
	}
	c.SetSelected(false)
	if c.badge.Text == "" {
		t.Error("badge.Text on selectable+unselected should be the bullet, not empty")
	}
}
