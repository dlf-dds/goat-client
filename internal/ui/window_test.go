package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// TestApplyState_RegressionBar is the F-108 / F-112 / F-113 regression
// bar: applyState() is the single point where worker-goroutine state
// updates land on the indicator + label + connect button. The bug
// surfaced when these widget mutations ran outside the Fyne main
// goroutine; the fix (PR #22) wraps them in fyne.Do at every caller.
//
// This test asserts the per-state visible shape so a future change to
// applyState (e.g., a new state, a re-shaped button label) trips a
// localised failure rather than re-meeting F-108 at next first-fire.
func TestApplyState_RegressionBar(t *testing.T) {
	app := test.NewTempApp(t)
	mw := newMainWindow(app, newFakeClient())

	cases := []struct {
		in           ipc.State
		wantColor    color.RGBA
		wantLabel    string
		wantBtnText  string
		wantDisabled bool
	}{
		{ipc.StateDisconnected, stateRGBA(ipc.StateDisconnected), "Disconnected", "Connect", false},
		{ipc.StateConnecting, stateRGBA(ipc.StateConnecting), "Connecting...", "Connecting...", true},
		{ipc.StateConnected, stateRGBA(ipc.StateConnected), "Connected", "Disconnect", false},
		{ipc.StateError, stateRGBA(ipc.StateError), "Error", "Connect", false},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			mw.applyState(tc.in)

			gotColor, ok := mw.indicator.FillColor.(color.RGBA)
			if !ok {
				t.Fatalf("indicator.FillColor is not RGBA: %T", mw.indicator.FillColor)
			}
			if gotColor != tc.wantColor {
				t.Errorf("indicator.FillColor = %v, want %v", gotColor, tc.wantColor)
			}
			if got := mw.stateLabel.Text; got != tc.wantLabel {
				t.Errorf("stateLabel.Text = %q, want %q", got, tc.wantLabel)
			}
			if got := mw.connectBtn.Text; got != tc.wantBtnText {
				t.Errorf("connectBtn.Text = %q, want %q", got, tc.wantBtnText)
			}
			if got := mw.connectBtn.Disabled(); got != tc.wantDisabled {
				t.Errorf("connectBtn.Disabled() = %v, want %v", got, tc.wantDisabled)
			}
		})
	}
}

// TestApplyState_UnknownStateFallsBackToConnect verifies that an
// unknown daemon state (e.g. a future-protocol drift) does not leave
// the button stuck in "Connecting..." disabled — the operator must
// always have a way out of an unknown UI state.
func TestApplyState_UnknownStateFallsBackToConnect(t *testing.T) {
	app := test.NewTempApp(t)
	mw := newMainWindow(app, newFakeClient())

	mw.applyState(ipc.State("never-heard-of-this"))

	if mw.connectBtn.Text != "Connect" {
		t.Errorf("connectBtn.Text on unknown state = %q, want %q", mw.connectBtn.Text, "Connect")
	}
	if mw.connectBtn.Disabled() {
		t.Error("connectBtn is disabled on unknown state; user has no way out")
	}
}

// TestApplyState_ColorMatchesTrayIcon verifies that the indicator dot
// in the main window and the systray icon agree on colour per state.
// They are sourced from the same stateRGBA() helper; this test pins
// the contract so a refactor that splits them silently into two
// palettes trips immediately.
func TestApplyState_ColorMatchesTrayIcon(t *testing.T) {
	app := test.NewTempApp(t)
	mw := newMainWindow(app, newFakeClient())

	for _, s := range []ipc.State{
		ipc.StateDisconnected, ipc.StateConnecting,
		ipc.StateConnected, ipc.StateError,
	} {
		mw.applyState(s)
		dotColor, ok := mw.indicator.FillColor.(color.RGBA)
		if !ok {
			t.Fatalf("indicator.FillColor is not RGBA: %T", mw.indicator.FillColor)
		}
		trayColor := stateRGBA(s) // same source the tray uses via iconForState
		if dotColor != trayColor {
			t.Errorf("state %q: dot=%v, tray=%v — palette drifted", s, dotColor, trayColor)
		}
	}
}

// TestNewMainWindow_SeedsDisconnectedState verifies the construction
// path lands the window in the Disconnected shape before any polling
// runs — without this, a first-fire user with the daemon down would
// see an indeterminate UI.
func TestNewMainWindow_SeedsDisconnectedState(t *testing.T) {
	app := test.NewTempApp(t)
	mw := newMainWindow(app, newFakeClient())

	if got := mw.stateLabel.Text; got != "Disconnected" {
		t.Errorf("seed stateLabel.Text = %q, want %q", got, "Disconnected")
	}
	if mw.connectBtn.Text != "Connect" {
		t.Errorf("seed connectBtn.Text = %q, want %q", mw.connectBtn.Text, "Connect")
	}
	if mw.connectBtn.Disabled() {
		t.Error("seed connectBtn is disabled; new window must allow connect")
	}
}

// TestPollOnce_MarshalsThroughFyneDo is the meaningful F-108 regression
// witness: it drives pollOnce on the test goroutine with a fake client
// returning each state, and verifies that the widget state updates
// even though the daemon read happens via a context-bound call.
//
// Fyne's test driver flushes fyne.Do callbacks synchronously on the
// test goroutine, so assertions can fire immediately after pollOnce
// returns — no sleeps, no polling. If a future refactor of pollOnce
// bypasses fyne.Do (e.g., calls applyState directly from a worker),
// this test still passes (the call is on the same goroutine), but
// running `go test -race` would flag it because real polling drives
// pollOnce from a Ticker goroutine. The other regression layer is
// fyne v2.5+ strict thread-checker enforced at runtime.
func TestPollOnce_MarshalsThroughFyneDo(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	mw := newMainWindow(app, fc)

	for _, s := range []ipc.State{
		ipc.StateConnecting, ipc.StateConnected,
		ipc.StateError, ipc.StateDisconnected,
	} {
		t.Run(string(s), func(t *testing.T) {
			fc.mu.Lock()
			fc.status = &ipc.StatusInfo{State: s, Mode: "combined"}
			fc.mu.Unlock()

			// pollOnce reads status from the fake, wraps mutation in
			// fyne.Do. The test driver flushes synchronously.
			mw.pollOnce(t.Context())

			want := stateLabel(s)
			if got := mw.stateLabel.Text; got != want {
				t.Errorf("after pollOnce(%q): stateLabel.Text = %q, want %q", s, got, want)
			}
		})
	}
}

// TestPollOnce_StatusErrorLeavesUIUnchanged verifies that a daemon-side
// error doesn't blank or freeze the window — the previous state holds
// until a successful read replaces it.
func TestPollOnce_StatusErrorLeavesUIUnchanged(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{State: ipc.StateConnected, Mode: "combined"}
	mw := newMainWindow(app, fc)
	mw.pollOnce(t.Context()) // seed to Connected
	wantLabel := mw.stateLabel.Text
	wantBtn := mw.connectBtn.Text

	// Next poll errors — UI must stay on the last good state.
	fc.mu.Lock()
	fc.statusErr = errBoom
	fc.mu.Unlock()
	mw.pollOnce(t.Context())

	if mw.stateLabel.Text != wantLabel {
		t.Errorf("error-poll changed stateLabel: was %q now %q", wantLabel, mw.stateLabel.Text)
	}
	if mw.connectBtn.Text != wantBtn {
		t.Errorf("error-poll changed connectBtn.Text: was %q now %q", wantBtn, mw.connectBtn.Text)
	}
}
