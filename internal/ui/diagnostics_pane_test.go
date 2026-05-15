package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

func TestDiagnosticsPane_Refresh_SeedsLogsAndProbe(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.diags = &ipc.Diagnostics{
		LogTail:   []string{"line one", "line two"},
		LastProbe: stableTime(),
		Reachable: true,
	}

	p := newDiagnosticsPane(fc)

	if got := p.logs.Text; !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Errorf("logs.Text = %q, want both lines", got)
	}
	if got := p.probeMsg.Text; !strings.Contains(got, "Reachable") {
		t.Errorf("probeMsg.Text = %q, want a Reachable line", got)
	}
}

func TestDiagnosticsPane_Refresh_DiagnosticsError(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.diagsErr = errBoom

	p := newDiagnosticsPane(fc)

	if !strings.Contains(p.probeMsg.Text, "Failed to fetch diagnostics") {
		t.Errorf("probeMsg.Text on diags error = %q", p.probeMsg.Text)
	}
}

func TestRenderProbe_NoLastProbe(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	p := newDiagnosticsPane(fc)

	p.renderProbe(&ipc.Diagnostics{}) // LastProbe.IsZero()
	if p.probeMsg.Text != "No probe yet" {
		t.Errorf("renderProbe(zero) = %q, want %q", p.probeMsg.Text, "No probe yet")
	}
}

func TestRenderProbe_UnreachableWithError(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	p := newDiagnosticsPane(fc)

	p.renderProbe(&ipc.Diagnostics{
		LastProbe:  stableTime(),
		Reachable:  false,
		ProbeError: "timeout dialing 1.2.3.4",
	})
	if !strings.Contains(p.probeMsg.Text, "Unreachable") {
		t.Errorf("probeMsg.Text = %q, want Unreachable", p.probeMsg.Text)
	}
	if !strings.Contains(p.probeMsg.Text, "timeout") {
		t.Errorf("probeMsg.Text = %q, want probe error in message", p.probeMsg.Text)
	}
}

func TestRenderProbe_UnreachableNoError(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	p := newDiagnosticsPane(fc)

	p.renderProbe(&ipc.Diagnostics{
		LastProbe: stableTime(),
		Reachable: false,
	})
	if !strings.Contains(p.probeMsg.Text, "Unreachable as of") {
		t.Errorf("probeMsg.Text = %q", p.probeMsg.Text)
	}
}

// TestDiagnosticsPane_RunProbe_MarshalsThroughFyneDo regresses the
// F-108 fix on diagnostics_pane.go:78-87 — the probe goroutine must
// marshal its UI mutations back through fyne.Do. The probeDoneForTest
// channel synchronises widget reads happen-after goroutine completion.
func TestDiagnosticsPane_RunProbe_MarshalsThroughFyneDo(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.diags = &ipc.Diagnostics{
		LogTail:   []string{"fresh log line"},
		LastProbe: stableTime(),
		Reachable: true,
	}

	p := newDiagnosticsPane(fc)
	done := make(chan struct{}, 1)
	p.probeDoneForTest = done

	p.runProbe()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("probe goroutine never signalled done")
	}

	if !strings.Contains(p.probeMsg.Text, "Reachable") {
		t.Errorf("probeMsg.Text after runProbe = %q, want Reachable", p.probeMsg.Text)
	}
	if !strings.Contains(p.logs.Text, "fresh log line") {
		t.Errorf("logs.Text after runProbe = %q, missing fresh log line", p.logs.Text)
	}
}

func TestDiagnosticsPane_RunProbe_ErrorPath(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.diagsErr = errBoom

	p := newDiagnosticsPane(fc)
	done := make(chan struct{}, 1)
	p.probeDoneForTest = done

	p.runProbe()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("probe goroutine never signalled done on error path")
	}

	if !strings.Contains(p.probeMsg.Text, "Probe failed") {
		t.Errorf("probeMsg.Text after error runProbe = %q", p.probeMsg.Text)
	}
}
