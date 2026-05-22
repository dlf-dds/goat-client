package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// seedProfiles sets up a fakeClient with two stored profiles, the
// first marked active. Mirrors the verdict-gate scenario: at least
// two profiles in one install, one switchable to the other.
func seedProfiles(c *fakeClient) {
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	c.profiles = []ipc.ProfileInfo{
		{Name: "Goat Prod", Slug: "goat-prod", Mode: "combined", Site: "kwt-aj-A", ExpiresAt: now.AddDate(1, 0, 0), CreatedAt: now, UpdatedAt: now, Active: true},
		{Name: "Cochlearis Dev", Slug: "cochlearis-dev", Mode: "netbird-only", Site: "lab-dev", ExpiresAt: now.AddDate(0, 6, 0), CreatedAt: now, UpdatedAt: now, Active: false},
	}
}

func TestProfilesPane_InitialState(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newProfilesPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	if !p.switchBtn.Disabled() || !p.renameBtn.Disabled() || !p.removeBtn.Disabled() {
		t.Errorf("buttons must be disabled before a selection is made: switch=%v rename=%v remove=%v",
			p.switchBtn.Disabled(), p.renameBtn.Disabled(), p.removeBtn.Disabled())
	}
}

// TestProfilesPane_RefreshPopulatesList exercises the read-only
// path: list profiles, render rows, no buttons enabled until the
// user selects something.
func TestProfilesPane_RefreshPopulatesList(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	seedProfiles(fc)
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newProfilesPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh()
	// Refresh runs synchronously here (no goroutines in Refresh
	// itself). The list rebuild happens through fyne.Do but in the
	// test app that resolves on the same goroutine.
	if len(p.profiles) != 2 {
		t.Fatalf("p.profiles len = %d, want 2", len(p.profiles))
	}
	if !p.profiles[0].Active || p.profiles[1].Active {
		t.Errorf("active flags wrong: %+v", p.profiles)
	}
}

// TestProfilesPane_SwitchInvokesIPC drives the switch action and
// confirms the IPC client receives the target slug. The fakeClient
// records SetActiveProfile calls; we assert the recorded slug
// matches the selection.
func TestProfilesPane_SwitchInvokesIPC(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	seedProfiles(fc)
	fc.setActiveReply = &ipc.ProfileInfo{Name: "Cochlearis Dev", Slug: "cochlearis-dev", Active: true}
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newProfilesPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh()
	p.selectedIdx = 1 // select inactive Cochlearis Dev
	p.updateButtonState()
	if p.switchBtn.Disabled() {
		t.Fatal("switchBtn should be enabled for the inactive selection")
	}
	done := make(chan struct{}, 1)
	p.refreshDoneForTest = done
	p.switchToSelected()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("switchToSelected did not complete within 2s")
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.setActiveCalls) != 1 || fc.setActiveCalls[0] != "cochlearis-dev" {
		t.Errorf("setActiveCalls = %v, want [cochlearis-dev]", fc.setActiveCalls)
	}
}

// TestProfilesPane_SwitchSurfacesError covers the failure path —
// the daemon returns an error (the actively-running profile failed
// to tear down, target profile is gone, etc.). The pane should
// surface the error via the status label without disabling itself
// permanently.
func TestProfilesPane_SwitchSurfacesError(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	seedProfiles(fc)
	fc.setActiveErr = errors.New("switch failed")
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newProfilesPane(fc)
	p.SetWindow(w)
	_ = p.Content()
	p.Refresh()
	p.selectedIdx = 1
	p.updateButtonState()
	done := make(chan struct{}, 1)
	p.refreshDoneForTest = done
	p.switchToSelected()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("switchToSelected did not complete within 2s")
	}
	if !strings.Contains(p.status.Text, "switch failed") {
		t.Errorf("status.Text = %q, want substring %q", p.status.Text, "switch failed")
	}
}

// TestProfilesPane_RemoveInvokesIPC covers the destructive path —
// fakeClient records the slug; the pane must NOT touch any other
// slug. Encodes the clobber-resistance gate at the UI layer.
func TestProfilesPane_RemoveInvokesIPC(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	seedProfiles(fc)
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newProfilesPane(fc)
	// No window → doRemove bypasses the confirm dialog. The headless
	// fast-path is what the tests target.
	p.SetWindow(nil)
	_ = p.Content()
	p.Refresh()
	p.selectedIdx = 1
	p.updateButtonState()
	done := make(chan struct{}, 1)
	p.refreshDoneForTest = done
	p.removeSelected()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("removeSelected did not complete within 2s")
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.removeProfileCalls) != 1 || fc.removeProfileCalls[0] != "cochlearis-dev" {
		t.Errorf("removeProfileCalls = %v, want [cochlearis-dev]", fc.removeProfileCalls)
	}
}
