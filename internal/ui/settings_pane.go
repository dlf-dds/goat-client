package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// settingsPane carries the v0.2 Settings → Mode selector. Three radio
// buttons (wg-cp0-only / netbird-only / combined) reflecting the
// daemon's currently active mode. Switching prompts "Reconnecting
// tunnels…" while the daemon reconciles, then refreshes the status
// pane.
//
// Held by mainWindow; lives in its own tab so the surface is
// discoverable without crowding the existing Bundle / Status / Diagnostics
// tabs.
type settingsPane struct {
	client ipc.Client
	win    fyne.Window

	radio       *widget.RadioGroup
	current     *widget.Label
	applyBtn    *widget.Button
	status      *widget.Label
	progressBar *widget.ProgressBarInfinite

	// onModeChanged fires after a successful setMode. The mainWindow
	// hooks this to refresh the tray + status pane.
	onModeChanged func(newMode string)

	// activeMode tracks the last-known daemon mode so Refresh() avoids
	// unnecessary radio churn while the user is mid-selection.
	activeMode string

	// applyDoneForTest is a test-only sync hook. The mode-switch
	// goroutine signals on this channel before exiting so tests can
	// deterministically observe widget state. Nil in production.
	applyDoneForTest chan<- struct{}
}

func newSettingsPane(client ipc.Client) *settingsPane {
	p := &settingsPane{
		client:      client,
		current:     widget.NewLabel("—"),
		status:      widget.NewLabel(""),
		progressBar: widget.NewProgressBarInfinite(),
	}
	p.progressBar.Stop()
	p.progressBar.Hide()

	options := []string{
		mode.WGCP0Only.Display(),
		mode.NetbirdOnly.Display(),
		mode.Combined.Display(),
	}
	p.radio = widget.NewRadioGroup(options, func(_ string) { /* no-op; Apply commits */ })

	p.applyBtn = widget.NewButton("Apply", func() { p.apply() })
	return p
}

// SetWindow lets the pane parent dialogs and toast messages off the main
// window (mirrors bundlePane's pattern).
func (p *settingsPane) SetWindow(w fyne.Window) { p.win = w }

func (p *settingsPane) SetOnModeChanged(fn func(string)) { p.onModeChanged = fn }

func (p *settingsPane) Content() fyne.CanvasObject {
	help := widget.NewLabel("Pick which tunnel subsystems goat-client runs. The daemon\nreconciles in <30s on switch; you can keep using the rest of\nthe UI while it does.")
	help.Wrapping = fyne.TextWrapWord
	header := container.NewVBox(
		widget.NewLabelWithStyle("Active mode", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.current,
	)
	body := container.NewVBox(
		help,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Switch to:", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		p.radio,
		container.NewHBox(p.applyBtn, p.progressBar),
		p.status,
	)
	return container.NewBorder(header, nil, nil, nil, body)
}

// Refresh queries the daemon for the active mode and seeds the radio.
// Safe to call from any goroutine; UI mutations are marshalled via
// fyne.Do.
func (p *settingsPane) Refresh() {
	if p.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m, err := p.client.GetMode(ctx)
	if err != nil {
		fyne.Do(func() {
			p.current.SetText("(failed: " + err.Error() + ")")
		})
		return
	}
	parsed, _ := mode.Parse(m)
	fyne.Do(func() {
		p.activeMode = m
		p.current.SetText(parsed.Display())
		p.radio.SetSelected(parsed.Display())
	})
}

func (p *settingsPane) apply() {
	selected := p.radio.Selected
	if selected == "" {
		return
	}
	var target mode.Mode
	for _, m := range mode.All() {
		if m.Display() == selected {
			target = m
			break
		}
	}
	if !target.Valid() {
		dialog.ShowError(fmt.Errorf("unknown selection: %s", selected), p.win)
		return
	}
	if string(target) == p.activeMode {
		p.status.SetText("Already in this mode.")
		return
	}
	p.applyBtn.Disable()
	p.progressBar.Show()
	p.progressBar.Start()
	p.status.SetText("Reconnecting tunnels…")

	go func() {
		defer func() {
			if p.applyDoneForTest != nil {
				p.applyDoneForTest <- struct{}{}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		prev, err := p.client.SetMode(ctx, string(target))
		fyne.Do(func() {
			p.progressBar.Stop()
			p.progressBar.Hide()
			p.applyBtn.Enable()
			if err != nil {
				p.status.SetText("Failed: " + err.Error())
				dialog.ShowError(err, p.win)
				return
			}
			p.activeMode = string(target)
			p.current.SetText(target.Display())
			p.status.SetText(fmt.Sprintf("Switched: %s → %s", prev, target))
			if p.onModeChanged != nil {
				p.onModeChanged(string(target))
			}
		})
	}()
}
