package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/ipc"
)

// makeTestBundle mints a minimal valid CBOR-encoded EnrollmentBundle.
// Signature is empty — bundle.Preview parses without verifying, which is
// the bundlePane's preview surface. The daemon does the real verify on
// Apply (covered by integration tests, not here).
func makeTestBundle(t *testing.T) []byte {
	t.Helper()
	pubkey := make([]byte, 32)
	privkey := make([]byte, 32)
	relayKey := make([]byte, 32)
	for i := range pubkey {
		pubkey[i] = byte(i + 1)
		privkey[i] = byte(i + 33)
		relayKey[i] = byte(i + 65)
	}
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	b := &bundle.EnrollmentBundle{
		Version:            bundle.Version,
		DeviceID:           "test-device-001",
		PeerPubkey:         pubkey,
		ACLGroups:          []string{"goats"},
		Site:               "lab-test",
		IssuedAt:           now,
		ActivationDeadline: now.Add(24 * time.Hour),
		ExpiresAt:          now.Add(90 * 24 * time.Hour),
		CAID:               "test-ca",
		CPDevicePubkey:     pubkey,
		CPDevicePrivkey:    privkey,
		CPDeviceAddress:    "198.18.0.6/24",
		KnownEndpoints: []bundle.KnownEndpoint{
			{Addr: "203.0.113.5:51820", Pubkey: relayKey, Kind: bundle.KindRelay},
		},
	}
	raw, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal test bundle: %v", err)
	}
	return raw
}

func TestBundlePane_InitialState(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	// Force GetStatus to report no bundle so refreshCurrentFromDaemon
	// leaves the seed label intact.
	fc.status = &ipc.StatusInfo{State: ipc.StateDisconnected, BundleImported: false}
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newBundlePane(fc)
	p.SetWindow(w)

	if got := p.currentLabel.Text; got != "No bundle imported." {
		t.Errorf("initial currentLabel.Text = %q, want %q", got, "No bundle imported.")
	}
	if !p.applyButton.Disabled() {
		t.Error("applyButton should be disabled before a bundle is loaded")
	}
	if p.pickButton.Disabled() {
		t.Error("pickButton should be enabled at construction so the user can pick")
	}
}

func TestBundlePane_LoadFromBytes_ShowsPreview(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	w := app.NewWindow("test")
	t.Cleanup(w.Close)
	p := newBundlePane(fc)
	p.SetWindow(w)

	raw := makeTestBundle(t)
	p.loadFromBytes(raw, "/tmp/test.cbor")

	if p.applyButton.Disabled() {
		t.Error("applyButton must be enabled after a valid preview")
	}
	preview := p.previewLabel.Text
	for _, want := range []string{
		"test-device-001", "lab-test", "203.0.113.5:51820", "/tmp/test.cbor",
	} {
		if !strings.Contains(preview, want) {
			t.Errorf("preview missing %q; got:\n%s", want, preview)
		}
	}
}

func TestBundlePane_LoadFromBytes_InvalidLeavesApplyDisabled(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	w := app.NewWindow("test")
	t.Cleanup(w.Close)
	p := newBundlePane(fc)
	p.SetWindow(w)

	// Garbage bytes — bundle.Preview returns error, no UI state change.
	p.loadFromBytes([]byte{0x01, 0x02, 0x03}, "/tmp/bad.cbor")

	if !p.applyButton.Disabled() {
		t.Error("applyButton must stay disabled when preview fails")
	}
	if p.currentRaw != nil {
		t.Errorf("currentRaw should be nil after invalid preview; got %d bytes", len(p.currentRaw))
	}
}

// TestBundlePane_Apply_Success exercises the F-108 regression bar for
// the bundle-import path: bundlePane.apply launches a goroutine, hands
// raw bytes to client.ImportBundle, and on success marshals UI updates
// back via fyne.Do. The test waits on applyDoneForTest so widget
// reads happen-after the goroutine's last fyne.Do callback completes
// (without the channel, the race detector flags the cross-goroutine
// reads on widget state).
func TestBundlePane_Apply_Success(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.bundleReply = &ipc.BundleInfo{
		IssuedTo:   "test-device-001",
		Site:       "lab-test",
		NotAfter:   time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
		PeerPubKey: "stub-pubkey-base64==",
		Endpoints:  []string{"203.0.113.5:51820"},
	}

	w := app.NewWindow("test")
	t.Cleanup(w.Close)
	p := newBundlePane(fc)
	p.SetWindow(w)

	done := make(chan struct{}, 1)
	p.applyDoneForTest = done

	applied := make(chan *ipc.BundleInfo, 1)
	p.SetOnApplied(func(info *ipc.BundleInfo) { applied <- info })

	raw := makeTestBundle(t)
	p.loadFromBytes(raw, "/tmp/test.cbor")
	p.apply()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("apply goroutine never signalled done")
	}

	select {
	case got := <-applied:
		if got == nil || got.IssuedTo != "test-device-001" {
			t.Errorf("onApplied info = %+v, want IssuedTo=test-device-001", got)
		}
	default:
		t.Fatal("onApplied callback was not invoked")
	}

	// After applyDoneForTest signals, all the apply-goroutine's writes
	// happen-before this read.
	if p.currentRaw != nil {
		t.Error("currentRaw should be cleared after a successful apply")
	}
	if p.previewLabel.Text != "" {
		t.Errorf("previewLabel.Text = %q, want empty after apply", p.previewLabel.Text)
	}
	if p.applyButton.Disabled() {
		// applyButton stays disabled-after-clear is OK; the test for
		// re-enable is the daemon-reject path. Success leaves the
		// "no current bundle" state where Apply is rightfully disabled
		// (this is the seed shape returned by newBundlePane).
		// Just confirm pickButton is back so the user can pick again.
		_ = p.applyButton.Disabled()
	}
	if p.pickButton.Disabled() {
		t.Error("pickButton must be re-enabled after a successful apply")
	}

	// The daemon got the raw bytes.
	imports := fc.snapshotImported()
	if len(imports) != 1 {
		t.Fatalf("expected 1 ImportBundle call, got %d", len(imports))
	}
	if len(imports[0]) != len(raw) {
		t.Errorf("ImportBundle got %d bytes, want %d", len(imports[0]), len(raw))
	}
}

// TestBundlePane_Apply_DaemonReject re-enables the buttons on error so
// the user can retry — regression bar for the F-108 fix at bundle_pane.go
// lines 169-174.
func TestBundlePane_Apply_DaemonReject(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.bundleErr = errBoom

	w := app.NewWindow("test")
	t.Cleanup(w.Close)
	p := newBundlePane(fc)
	p.SetWindow(w)

	done := make(chan struct{}, 1)
	p.applyDoneForTest = done

	p.loadFromBytes(makeTestBundle(t), "/tmp/test.cbor")
	p.apply()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("apply goroutine never signalled done after daemon reject")
	}

	if p.applyButton.Disabled() {
		t.Error("applyButton must be re-enabled after daemon reject so the user can retry")
	}
	if p.pickButton.Disabled() {
		t.Error("pickButton must be re-enabled after daemon reject")
	}
}

func TestFormatBundlePreview_ContainsAllFields(t *testing.T) {
	meta := &bundle.Metadata{
		IssuedTo:   "dev-1",
		Site:       "site-1",
		NotBefore:  time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC),
		NotAfter:   time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		PeerPubKey: "abc==",
		Endpoints:  []string{"a:1", "b:2"},
	}
	s := formatBundlePreview(meta, "/tmp/x.cbor", 1024)
	for _, want := range []string{
		"/tmp/x.cbor", "1024 bytes",
		"dev-1", "site-1", "abc==",
		"2026-05-14", "2026-08-14",
		"a:1", "b:2",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("preview missing %q; got:\n%s", want, s)
		}
	}
}

func TestFormatBundleCurrent_ContainsAllFields(t *testing.T) {
	info := &ipc.BundleInfo{
		IssuedTo:   "dev-2",
		Site:       "site-2",
		NotAfter:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		PeerPubKey: "xyz==",
		Endpoints:  []string{"x:9"},
	}
	s := formatBundleCurrent(info)
	for _, want := range []string{"dev-2", "site-2", "xyz==", "x:9", "2026-09-01"} {
		if !strings.Contains(s, want) {
			t.Errorf("current label missing %q; got:\n%s", want, s)
		}
	}
}

// TestBundlePane_RefreshCurrentFromDaemon_SeedsFromStatus verifies the
// "daemon already has a bundle" path the constructor follows. If the
// daemon reports BundleImported=true with a Bundle, the pane's
// current label should reflect it on first show.
func TestBundlePane_RefreshCurrentFromDaemon_SeedsFromStatus(t *testing.T) {
	app := test.NewTempApp(t)
	fc := newFakeClient()
	fc.status = &ipc.StatusInfo{
		State:          ipc.StateConnected,
		BundleImported: true,
		Bundle: &ipc.BundleInfo{
			IssuedTo:   "already-here",
			Site:       "production",
			NotAfter:   time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			PeerPubKey: "pre-imported==",
			Endpoints:  []string{"prod:51820"},
		},
	}
	w := app.NewWindow("test")
	t.Cleanup(w.Close)

	p := newBundlePane(fc)
	p.SetWindow(w)

	if !strings.Contains(p.currentLabel.Text, "already-here") {
		t.Errorf("currentLabel did not seed from daemon status; got: %q", p.currentLabel.Text)
	}
}
