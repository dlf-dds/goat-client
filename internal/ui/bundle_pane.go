package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/ipc"
)

// bundlePane drives the offline-CA bundle import flow:
//
//  1. user picks a file (button OR drags it onto the window)
//  2. pane parses preview metadata locally and renders it
//  3. on Apply, pane hands raw bytes to the daemon via ipc.ImportBundle,
//     which is the authoritative parse + Ed25519 verify step
//  4. window refreshes status pane afterwards
type bundlePane struct {
	window fyne.Window
	client ipc.Client

	current     *bundle.Metadata
	currentRaw  []byte
	currentPath string

	currentLabel *widget.Label
	previewLabel *widget.Label
	pickButton   *widget.Button
	applyButton  *widget.Button

	root fyne.CanvasObject

	onApplied func(*ipc.BundleInfo)

	// applyDoneForTest is a test-only sync hook. The bundle-import
	// goroutine signals on this channel right before exiting so tests
	// can deterministically wait for the work to complete (the race
	// detector would otherwise flag widget reads from the test
	// goroutine against widget writes from the apply goroutine).
	// Production code never sets this field; the goroutine no-ops on
	// the signal when it's nil.
	applyDoneForTest chan<- struct{}
}

func newBundlePane(client ipc.Client) *bundlePane {
	p := &bundlePane{
		client:       client,
		currentLabel: widget.NewLabel("No bundle imported."),
		previewLabel: widget.NewLabel(""),
	}
	p.currentLabel.Wrapping = fyne.TextWrapWord
	p.previewLabel.Wrapping = fyne.TextWrapWord

	p.pickButton = widget.NewButton("Choose bundle file...", func() { p.openFilePicker() })
	p.applyButton = widget.NewButton("Apply", func() { p.apply() })
	p.applyButton.Disable()

	hint := widget.NewLabel("Drag a bundle file onto this window, or click Choose bundle file...")
	hint.Wrapping = fyne.TextWrapWord

	buttons := container.NewHBox(p.pickButton, p.applyButton)
	p.root = container.NewVBox(
		p.currentLabel,
		widget.NewSeparator(),
		hint,
		buttons,
		widget.NewSeparator(),
		widget.NewLabel("Preview:"),
		p.previewLabel,
	)
	p.refreshCurrentFromDaemon()
	return p
}

// SetWindow wires the bundle pane to its parent window so dialogs (file
// picker, error popups) have a parent and so window-level drag-drop can
// route here.
func (p *bundlePane) SetWindow(w fyne.Window) {
	p.window = w
}

// SetOnApplied registers a callback fired after a successful Apply so the
// host can refresh sibling panes (status, diagnostics).
func (p *bundlePane) SetOnApplied(cb func(*ipc.BundleInfo)) {
	p.onApplied = cb
}

func (p *bundlePane) Content() fyne.CanvasObject { return p.root }

// HandleDroppedFiles is called by the window's OnDropped hook with the
// URIs of any files the user dragged onto the window. Only the first file
// is treated as a bundle.
func (p *bundlePane) HandleDroppedFiles(uris []fyne.URI) {
	if len(uris) == 0 {
		return
	}
	p.loadFromPath(uris[0].Path())
}

func (p *bundlePane) openFilePicker() {
	if p.window == nil {
		return
	}
	d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, p.window)
			return
		}
		if rc == nil {
			return
		}
		defer rc.Close()
		raw, err := io.ReadAll(rc)
		if err != nil {
			dialog.ShowError(fmt.Errorf("read bundle: %w", err), p.window)
			return
		}
		p.loadFromBytes(raw, rc.URI().Path())
	}, p.window)
	if home, err := os.UserHomeDir(); err == nil {
		if uri, err := storage.ListerForURI(storage.NewFileURI(home)); err == nil {
			d.SetLocation(uri)
		}
	}
	d.Show()
}

func (p *bundlePane) loadFromPath(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if p.window != nil {
			dialog.ShowError(fmt.Errorf("read %s: %w", path, err), p.window)
		}
		return
	}
	p.loadFromBytes(raw, path)
}

func (p *bundlePane) loadFromBytes(raw []byte, sourcePath string) {
	meta, err := bundle.Preview(raw)
	if err != nil {
		if p.window != nil {
			dialog.ShowError(fmt.Errorf("preview bundle: %w", err), p.window)
		}
		return
	}
	p.current = meta
	p.currentRaw = raw
	p.currentPath = sourcePath
	p.previewLabel.SetText(formatBundlePreview(meta, sourcePath, len(raw)))
	p.applyButton.Enable()
}

func (p *bundlePane) apply() {
	if p.client == nil || p.currentRaw == nil {
		return
	}
	p.applyButton.Disable()
	p.pickButton.Disable()
	go func() {
		// All UI mutations below run on the import goroutine and must
		// marshal back to the Fyne main goroutine via fyne.Do (F-108).
		defer func() {
			if p.applyDoneForTest != nil {
				p.applyDoneForTest <- struct{}{}
			}
		}()
		defer fyne.Do(func() { p.pickButton.Enable() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		info, err := p.client.ImportBundle(ctx, p.currentRaw)
		if err != nil {
			fyne.Do(func() {
				if p.window != nil {
					dialog.ShowError(fmt.Errorf("daemon rejected bundle: %w", err), p.window)
				}
				p.applyButton.Enable()
			})
			return
		}
		fyne.Do(func() {
			p.currentLabel.SetText(formatBundleCurrent(info))
			p.previewLabel.SetText("")
			p.currentRaw = nil
			p.current = nil
			p.currentPath = ""
			if p.onApplied != nil {
				p.onApplied(info)
			}
		})
	}()
}

func (p *bundlePane) refreshCurrentFromDaemon() {
	if p.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := p.client.GetStatus(ctx)
	if err != nil || st == nil || !st.BundleImported || st.Bundle == nil {
		return
	}
	p.currentLabel.SetText(formatBundleCurrent(st.Bundle))
}

func formatBundlePreview(meta *bundle.Metadata, sourcePath string, size int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Source: %s (%d bytes)\n", sourcePath, size)
	fmt.Fprintf(&sb, "Issued to: %s\n", meta.IssuedTo)
	fmt.Fprintf(&sb, "Site:      %s\n", meta.Site)
	fmt.Fprintf(&sb, "Validity:  %s — %s\n", meta.NotBefore.Format(time.RFC3339), meta.NotAfter.Format(time.RFC3339))
	fmt.Fprintf(&sb, "Peer pubkey: %s\n", meta.PeerPubKey)
	fmt.Fprintf(&sb, "Endpoints:\n  %s", strings.Join(meta.Endpoints, "\n  "))
	return sb.String()
}

func formatBundleCurrent(info *ipc.BundleInfo) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Imported bundle:\n")
	fmt.Fprintf(&sb, "  Issued to: %s\n", info.IssuedTo)
	fmt.Fprintf(&sb, "  Site:      %s\n", info.Site)
	fmt.Fprintf(&sb, "  Expires:   %s\n", info.NotAfter.Format(time.RFC3339))
	fmt.Fprintf(&sb, "  Peer pubkey: %s\n", info.PeerPubKey)
	fmt.Fprintf(&sb, "  Endpoints:   %s", strings.Join(info.Endpoints, ", "))
	return sb.String()
}
