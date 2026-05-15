package ui

import (
	"context"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// diagnosticsPane renders the WireGuard log tail + a "test connection"
// button that asks the daemon for a fresh reachability probe and surfaces
// the result inline.
type diagnosticsPane struct {
	client ipc.Client

	logs     *widget.Entry
	probeMsg *widget.Label
	root     fyne.CanvasObject

	// probeDoneForTest is a test-only sync hook. The probe goroutine
	// signals on this channel before exiting so tests can deterministically
	// observe widget state. Nil in production.
	probeDoneForTest chan<- struct{}
}

func newDiagnosticsPane(client ipc.Client) *diagnosticsPane {
	p := &diagnosticsPane{
		client:   client,
		logs:     widget.NewMultiLineEntry(),
		probeMsg: widget.NewLabel(""),
	}
	p.logs.Disable()
	p.logs.Wrapping = fyne.TextWrapOff
	p.probeMsg.Wrapping = fyne.TextWrapWord

	test := widget.NewButton("Test connection", func() { p.runProbe() })
	refresh := widget.NewButton("Refresh", func() { p.Refresh() })
	buttons := container.NewHBox(test, refresh)

	p.root = container.NewBorder(
		nil,
		container.NewVBox(p.probeMsg, buttons),
		nil, nil,
		container.NewScroll(p.logs),
	)
	p.Refresh()
	return p
}

func (p *diagnosticsPane) Content() fyne.CanvasObject { return p.root }

func (p *diagnosticsPane) Refresh() {
	if p.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d, err := p.client.GetDiagnostics(ctx)
	if err != nil {
		p.probeMsg.SetText("Failed to fetch diagnostics: " + err.Error())
		return
	}
	p.logs.SetText(strings.Join(d.LogTail, "\n"))
	p.renderProbe(d)
}

func (p *diagnosticsPane) runProbe() {
	if p.client == nil {
		return
	}
	p.probeMsg.SetText("Probing...")
	go func() {
		// UI mutations from the probe goroutine must marshal back to
		// the Fyne main goroutine via fyne.Do (F-108).
		defer func() {
			if p.probeDoneForTest != nil {
				p.probeDoneForTest <- struct{}{}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d, err := p.client.GetDiagnostics(ctx)
		if err != nil {
			fyne.Do(func() {
				p.probeMsg.SetText("Probe failed: " + err.Error())
			})
			return
		}
		fyne.Do(func() {
			p.renderProbe(d)
			p.logs.SetText(strings.Join(d.LogTail, "\n"))
		})
	}()
}

func (p *diagnosticsPane) renderProbe(d *ipc.Diagnostics) {
	if d.LastProbe.IsZero() {
		p.probeMsg.SetText("No probe yet")
		return
	}
	when := d.LastProbe.Format(time.RFC3339)
	switch {
	case d.Reachable:
		p.probeMsg.SetText("Reachable as of " + when)
	case d.ProbeError != "":
		p.probeMsg.SetText("Unreachable as of " + when + ": " + d.ProbeError)
	default:
		p.probeMsg.SetText("Unreachable as of " + when)
	}
}
