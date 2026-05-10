package ui

import (
	"context"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// mainWindow is the goat-client GUI's primary window: a status header
// (state indicator + label + connect/disconnect button) above tabs for
// Bundle / Status / Diagnostics. The header reflects the same state the
// systray icon shows; the two are kept in sync by polling the daemon.
type mainWindow struct {
	app    fyne.App
	win    fyne.Window
	client ipc.Client
	notify *notifier

	indicator   *canvas.Circle
	stateLabel  *widget.Label
	connectBtn  *widget.Button
	statusPane  *statusPane
	diagsPane   *diagnosticsPane
	bundlePane  *bundlePane
	tabs        *container.AppTabs
	pollCancel  context.CancelFunc
}

func newMainWindow(a fyne.App, client ipc.Client) *mainWindow {
	w := a.NewWindow("goat-client")
	w.Resize(fyne.NewSize(640, 480))

	mw := &mainWindow{
		app:        a,
		win:        w,
		client:     client,
		notify:     newNotifier(a),
		indicator:  canvas.NewCircle(stateColor(ipc.StateDisconnected)),
		stateLabel: widget.NewLabel(stateLabel(ipc.StateDisconnected)),
	}
	mw.indicator.Resize(fyne.NewSize(18, 18))

	mw.connectBtn = widget.NewButton("Connect", func() { mw.toggleConnection() })

	mw.statusPane = newStatusPane(client)
	mw.diagsPane = newDiagnosticsPane(client)
	mw.bundlePane = newBundlePane(client)
	mw.bundlePane.SetWindow(w)
	mw.bundlePane.SetOnApplied(func(_ *ipc.BundleInfo) {
		mw.notify.Send("Bundle applied", "Tunnel configuration updated.")
		mw.statusPane.Refresh()
	})

	mw.tabs = container.NewAppTabs(
		container.NewTabItem("Bundle", mw.bundlePane.Content()),
		container.NewTabItem("Status", mw.statusPane.Content()),
		container.NewTabItem("Diagnostics", mw.diagsPane.Content()),
	)

	header := container.NewBorder(
		nil, nil,
		container.NewHBox(mw.indicator, mw.stateLabel),
		mw.connectBtn,
		nil,
	)

	w.SetContent(container.NewBorder(header, nil, nil, nil, mw.tabs))
	w.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		mw.tabs.SelectIndex(0)
		mw.bundlePane.HandleDroppedFiles(uris)
	})

	return mw
}

func (m *mainWindow) Show() {
	m.startPolling()
	m.win.SetCloseIntercept(func() {
		m.stopPolling()
		m.win.Close()
	})
	m.win.ShowAndRun()
}

func (m *mainWindow) startPolling() {
	ctx, cancel := context.WithCancel(context.Background())
	m.pollCancel = cancel
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.pollOnce(ctx)
			}
		}
	}()
}

func (m *mainWindow) stopPolling() {
	if m.pollCancel != nil {
		m.pollCancel()
		m.pollCancel = nil
	}
}

// pollOnce reads the daemon status (from a worker goroutine) and
// marshals every UI mutation back onto the Fyne main goroutine via
// fyne.Do — Fyne v2.5+ strict thread-checker rejects widget mutations
// from non-main goroutines (F-108).
func (m *mainWindow) pollOnce(ctx context.Context) {
	st, err := m.client.GetStatus(ctx)
	if err != nil {
		return
	}
	fyne.Do(func() {
		m.applyState(st.State)
		if m.tabs != nil {
			switch m.tabs.SelectedIndex() {
			case 1:
				m.statusPane.apply(st)
			case 2:
				m.diagsPane.Refresh()
			}
		}
	})
}

// applyState mutates header widgets to reflect the current daemon state.
// Caller is responsible for running this on the Fyne main goroutine
// (wrap with fyne.Do if calling from a worker). pollOnce already wraps.
func (m *mainWindow) applyState(s ipc.State) {
	m.indicator.FillColor = stateColor(s)
	m.indicator.Refresh()
	m.stateLabel.SetText(stateLabel(s))
	switch s {
	case ipc.StateConnected:
		m.connectBtn.SetText("Disconnect")
		m.connectBtn.Enable()
	case ipc.StateConnecting:
		m.connectBtn.SetText("Connecting...")
		m.connectBtn.Disable()
	default:
		m.connectBtn.SetText("Connect")
		m.connectBtn.Enable()
	}
}

func (m *mainWindow) toggleConnection() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := m.client.GetStatus(ctx)
	if err != nil {
		dialog.ShowError(err, m.win)
		return
	}
	go func() {
		opCtx, opCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer opCancel()
		var opErr error
		if st.State == ipc.StateConnected || st.State == ipc.StateConnecting {
			opErr = m.client.Disconnect(opCtx)
		} else {
			opErr = m.client.Connect(opCtx)
		}
		if opErr != nil {
			// dialog.ShowError + notifier touch UI; marshal to main goroutine.
			fyne.Do(func() {
				dialog.ShowError(opErr, m.win)
				m.notify.Send("Tunnel error", opErr.Error())
			})
		}
		m.pollOnce(opCtx) // pollOnce already wraps its own UI mutations.
	}()
}

// stateColor maps an IPC state to the indicator-circle fill colour. Same
// palette as the systray icon (see icons.go).
func stateColor(s ipc.State) color.Color {
	switch s {
	case ipc.StateConnected:
		return color.RGBA{R: 0x2c, G: 0xa0, B: 0x2c, A: 0xff}
	case ipc.StateConnecting:
		return color.RGBA{R: 0xe6, G: 0x9f, B: 0x00, A: 0xff}
	case ipc.StateError:
		return color.RGBA{R: 0xc0, G: 0x39, B: 0x2b, A: 0xff}
	default:
		return color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
	}
}
