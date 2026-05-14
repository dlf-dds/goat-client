package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// statusPane renders the mode-aware status surface.
//
//   - wg-cp0-only: single card showing the outer tunnel.
//   - netbird-only: single card showing the inner mesh.
//   - combined: two stacked cards (wg-cp0 outer + netbird inner) with a
//     small selected-for-diagnostics badge on the user-selected card.
//
// The card shapes share a layout helper (tunnelCard); only the source
// of truth differs (StatusInfo vs StatusInfo.InnerMesh).
type statusPane struct {
	client ipc.Client

	wg     *tunnelCard
	mesh   *tunnelCard
	stack  *fyne.Container
	header *widget.Label
	root   fyne.CanvasObject

	errorMessage *widget.Label

	// selected is the diagnostics-focus card identifier ("wg-cp0" or
	// "netbird"); only meaningful in combined mode. Defaults to "wg-cp0".
	selected string
}

func newStatusPane(client ipc.Client) *statusPane {
	p := &statusPane{
		client:       client,
		errorMessage: widget.NewLabel(""),
		header:       widget.NewLabel(""),
		selected:     "wg-cp0",
	}
	p.errorMessage.Wrapping = fyne.TextWrapWord
	p.header.TextStyle = fyne.TextStyle{Italic: true}

	p.wg = newTunnelCard("wg-cp0 (outer)", func() { p.selected = "wg-cp0"; p.applySelectionBadge() })
	p.mesh = newTunnelCard("netbird (inner mesh)", func() { p.selected = "netbird"; p.applySelectionBadge() })

	p.stack = container.NewVBox()

	refresh := widget.NewButton("Refresh", func() { p.Refresh() })
	footer := container.NewVBox(p.errorMessage, refresh)
	p.root = container.NewBorder(p.header, footer, nil, nil, p.stack)
	p.Refresh()
	return p
}

func (p *statusPane) Content() fyne.CanvasObject { return p.root }

func (p *statusPane) Refresh() {
	if p.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := p.client.GetStatus(ctx)
	if err != nil {
		p.errorMessage.SetText("Failed to fetch status: " + err.Error())
		return
	}
	p.errorMessage.SetText("")
	p.apply(st)
}

// apply mutates the pane to reflect st. Caller is responsible for
// running on the Fyne main goroutine (window.go's pollOnce wraps).
func (p *statusPane) apply(st *ipc.StatusInfo) {
	m, _ := mode.Parse(st.Mode)
	if !m.Valid() {
		m = mode.WGCP0Only
	}
	p.header.SetText("Active mode: " + m.Display())

	// Reconfigure the stack to match the mode. Cheap to rebuild — Fyne
	// reuses widgets so the tunnelCard instances persist across mode
	// transitions.
	p.stack.RemoveAll()
	if m.IncludesWGCP0() {
		p.stack.Add(p.wg.Content())
	}
	if m.IncludesNetbird() {
		p.stack.Add(p.mesh.Content())
	}
	if m == mode.Combined {
		p.wg.SetSelectable(true)
		p.mesh.SetSelectable(true)
	} else {
		p.wg.SetSelectable(false)
		p.mesh.SetSelectable(false)
		if m == mode.WGCP0Only {
			p.selected = "wg-cp0"
		} else {
			p.selected = "netbird"
		}
	}
	p.applySelectionBadge()
	p.stack.Refresh()

	if m.IncludesWGCP0() {
		p.wg.SetState(stateLabel(st.State))
		p.wg.SetInterface(orDash(st.InterfaceName))
		p.wg.SetLastHandshake(formatHandshake(st.LastHandshake))
		p.wg.SetBytes(st.BytesIn, st.BytesOut)
		p.wg.SetPeer(orDash(st.PeerPubKey))
		if len(st.Endpoints) == 0 {
			p.wg.SetEndpoints("—")
		} else {
			p.wg.SetEndpoints(strings.Join(st.Endpoints, "\n"))
		}
	}
	if m.IncludesNetbird() {
		if st.InnerMesh == nil {
			p.mesh.SetState(stateLabel(ipc.StateDisconnected))
			p.mesh.SetPeerCount(0)
			p.mesh.SetLastHandshake("never")
			p.mesh.SetBytes(0, 0)
		} else {
			p.mesh.SetState(stateLabel(st.InnerMesh.State))
			p.mesh.SetPeerCount(st.InnerMesh.PeerCount)
			p.mesh.SetLastHandshake(formatHandshake(st.InnerMesh.LastHandshake))
			p.mesh.SetBytes(st.InnerMesh.BytesIn, st.InnerMesh.BytesOut)
		}
	}

	if st.ErrorMessage != "" {
		p.errorMessage.SetText(st.ErrorMessage)
	}
}

func (p *statusPane) applySelectionBadge() {
	p.wg.SetSelected(p.selected == "wg-cp0")
	p.mesh.SetSelected(p.selected == "netbird")
}

func formatHandshake(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%s (%s ago)", t.Format(time.RFC3339), time.Since(t).Round(time.Second))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// tunnelCard is the per-leg status card. Two instances live inside the
// statusPane; only the wg-cp0 card carries the Peer / Endpoints rows.
type tunnelCard struct {
	title         *widget.Label
	badge         *widget.Label
	state         *widget.Label
	iface         *widget.Label
	lastHandshake *widget.Label
	bytesIn       *widget.Label
	bytesOut      *widget.Label
	peer          *widget.Label
	endpoints     *widget.Label
	peerCount     *widget.Label

	root       fyne.CanvasObject
	selectable bool
	onSelect   func()
}

func newTunnelCard(title string, onSelect func()) *tunnelCard {
	c := &tunnelCard{
		title:         widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		badge:         widget.NewLabel(""),
		state:         widget.NewLabel("—"),
		iface:         widget.NewLabel("—"),
		lastHandshake: widget.NewLabel("—"),
		bytesIn:       widget.NewLabel("—"),
		bytesOut:      widget.NewLabel("—"),
		peer:          widget.NewLabel("—"),
		endpoints:     widget.NewLabel("—"),
		peerCount:     widget.NewLabel("—"),
		onSelect:      onSelect,
	}
	c.peer.Wrapping = fyne.TextWrapBreak
	c.endpoints.Wrapping = fyne.TextWrapWord

	form := container.New(layoutForm(),
		widget.NewLabel("State:"), c.state,
		widget.NewLabel("Interface:"), c.iface,
		widget.NewLabel("Last handshake:"), c.lastHandshake,
		widget.NewLabel("Bytes in:"), c.bytesIn,
		widget.NewLabel("Bytes out:"), c.bytesOut,
		widget.NewLabel("Peer pubkey:"), c.peer,
		widget.NewLabel("Endpoints:"), c.endpoints,
		widget.NewLabel("Peer count:"), c.peerCount,
	)

	selectBtn := widget.NewButton("Select for diagnostics", func() {
		if c.onSelect != nil {
			c.onSelect()
		}
	})
	header := container.NewBorder(nil, nil, c.title, c.badge)
	c.root = widget.NewCard("", "", container.NewVBox(header, form, selectBtn))
	return c
}

func (c *tunnelCard) Content() fyne.CanvasObject { return c.root }

func (c *tunnelCard) SetState(s string)         { c.state.SetText(s) }
func (c *tunnelCard) SetInterface(s string)     { c.iface.SetText(s) }
func (c *tunnelCard) SetLastHandshake(s string) { c.lastHandshake.SetText(s) }
func (c *tunnelCard) SetBytes(in, out uint64) {
	c.bytesIn.SetText(fmt.Sprintf("%d", in))
	c.bytesOut.SetText(fmt.Sprintf("%d", out))
}
func (c *tunnelCard) SetPeer(s string)      { c.peer.SetText(s) }
func (c *tunnelCard) SetEndpoints(s string) { c.endpoints.SetText(s) }
func (c *tunnelCard) SetPeerCount(n int)    { c.peerCount.SetText(fmt.Sprintf("%d", n)) }

// SetSelectable hides the "Select for diagnostics" button when the card
// is the only leg in the current mode.
func (c *tunnelCard) SetSelectable(b bool) { c.selectable = b }

// SetSelected paints the diagnostics-focus badge on the active card.
func (c *tunnelCard) SetSelected(b bool) {
	if !c.selectable {
		c.badge.SetText("")
		return
	}
	if b {
		c.badge.SetText("◉ selected for diagnostics")
	} else {
		c.badge.SetText("○")
	}
}
