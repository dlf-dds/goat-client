package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/mode"
)

func TestSettingsPane_Refresh_SeedsCurrentMode(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.curMode = string(mode.Combined)
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newSettingsPane(fc)
	p.SetWindow(w)
	// Render the pane so the radio is realized.
	_ = p.Content()
	p.Refresh()

	if p.current.Text != mode.Combined.Display() {
		t.Errorf("current.Text = %q, want %q", p.current.Text, mode.Combined.Display())
	}
	if p.radio.Selected != mode.Combined.Display() {
		t.Errorf("radio.Selected = %q, want %q", p.radio.Selected, mode.Combined.Display())
	}
	if p.activeMode != string(mode.Combined) {
		t.Errorf("activeMode = %q, want %q", p.activeMode, string(mode.Combined))
	}
}

func TestSettingsPane_Refresh_GetModeError(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.modeErr = errBoom
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newSettingsPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh()

	if !strings.Contains(p.current.Text, "failed") {
		t.Errorf("current.Text on error = %q, want failure message", p.current.Text)
	}
}

// TestSettingsPane_Apply_NoOpWhenSameMode regresses the "Already in
// this mode" early-return in settings_pane.go:130-132 so a future
// refactor that drops the equality check doesn't unnecessarily fire
// a daemon mode-switch (which tears down + brings up tunnels —
// expensive).
func TestSettingsPane_Apply_NoOpWhenSameMode(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.curMode = string(mode.WGCP0Only)
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newSettingsPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh() // seeds activeMode

	p.radio.SetSelected(mode.WGCP0Only.Display())
	p.apply()

	// Wait briefly in case any goroutine fires.
	time.Sleep(50 * time.Millisecond)

	if got := len(fc.setModeCalls); got != 0 {
		t.Errorf("SetMode was called %d times for a no-op apply; want 0 (calls=%v)", got, fc.setModeCalls)
	}
	if !strings.Contains(p.status.Text, "Already") {
		t.Errorf("status.Text = %q, want 'Already in this mode'", p.status.Text)
	}
}

// TestSettingsPane_Apply_SwitchSucceeds exercises the F-108 regression
// bar for settings_pane.go:139-159 — the apply goroutine marshals
// status text + progress bar + callback through fyne.Do.
func TestSettingsPane_Apply_SwitchSucceeds(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.curMode = string(mode.WGCP0Only)
	fc.setModePrev = string(mode.WGCP0Only) // SetMode reports prev=wg-cp0-only
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newSettingsPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh()

	done := make(chan struct{}, 1)
	p.applyDoneForTest = done

	changed := make(chan string, 1)
	p.SetOnModeChanged(func(s string) { changed <- s })

	p.radio.SetSelected(mode.Combined.Display())
	p.apply()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("settings apply goroutine never signalled done")
	}

	if got := len(fc.setModeCalls); got != 1 {
		t.Fatalf("SetMode call count = %d, want 1", got)
	}
	if fc.setModeCalls[0] != string(mode.Combined) {
		t.Errorf("SetMode arg = %q, want %q", fc.setModeCalls[0], string(mode.Combined))
	}
	select {
	case got := <-changed:
		if got != string(mode.Combined) {
			t.Errorf("onModeChanged called with %q, want %q", got, string(mode.Combined))
		}
	default:
		t.Error("onModeChanged callback not invoked")
	}
	if p.activeMode != string(mode.Combined) {
		t.Errorf("activeMode after apply = %q, want %q", p.activeMode, string(mode.Combined))
	}
	if p.applyBtn.Disabled() {
		t.Error("applyBtn should be re-enabled after a successful switch")
	}
}

func TestSettingsPane_Apply_SwitchFails(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.curMode = string(mode.WGCP0Only)
	fc.setModeErr = errBoom
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newSettingsPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh()

	done := make(chan struct{}, 1)
	p.applyDoneForTest = done

	p.radio.SetSelected(mode.Combined.Display())
	p.apply()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("settings apply goroutine never signalled done on error path")
	}

	if !strings.Contains(p.status.Text, "Failed") {
		t.Errorf("status.Text = %q after failed setMode, want 'Failed: ...'", p.status.Text)
	}
	// activeMode must stay on the previous value — the switch didn't
	// land, so a follow-up Refresh shouldn't think we already moved.
	if p.activeMode != string(mode.WGCP0Only) {
		t.Errorf("activeMode after failed switch = %q, want %q (unchanged)", p.activeMode, string(mode.WGCP0Only))
	}
	if p.applyBtn.Disabled() {
		t.Error("applyBtn should be re-enabled after a failed switch")
	}
}

func TestSettingsPane_Apply_EmptySelectionNoOp(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newSettingsPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	// Don't call Refresh; radio is unselected.
	p.apply()

	time.Sleep(20 * time.Millisecond)

	if got := len(fc.setModeCalls); got != 0 {
		t.Errorf("SetMode called %d times with no radio selection; want 0", got)
	}
}
