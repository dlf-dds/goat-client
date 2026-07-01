package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// devicesHistoryCap bounds the per-peer RTT series the pane accumulates
// across refreshes to feed the latency chart.
const devicesHistoryCap = 60

// devicesPane is the connectivity-check surface: a roster of inner-mesh
// peers on the left, and for the selected peer a detail view with the
// direct/relayed badge, identity, live RTT stats, and a latency
// sparkline. Live RTT comes from the daemon's peerping subsystem; the
// pane accumulates each poll's reading into a local per-peer series so
// the chart shows a trend even though each reply carries only summary
// stats.
type devicesPane struct {
	client ipc.Client

	peers    []ipc.PeerConnectivity
	selected int
	history  map[string][]float64

	list *widget.List

	// detail widgets
	detailName   *widget.Label
	detailBadge  *widget.Label
	detailIdent  *widget.Label
	detailRTT    *widget.Label
	chart        *latencyChart
	detailWrap   *fyne.Container
	emptyNote    *widget.Label
	errorMessage *widget.Label
	root         fyne.CanvasObject
}

func newDevicesPane(client ipc.Client) *devicesPane {
	p := &devicesPane{
		client:       client,
		selected:     -1,
		history:      map[string][]float64{},
		detailName:   widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		detailBadge:  widget.NewLabel(""),
		detailIdent:  widget.NewLabel(""),
		detailRTT:    widget.NewLabel(""),
		chart:        newLatencyChart(),
		emptyNote:    widget.NewLabel("No inner-mesh peers. The connectivity check is available in netbird-only / combined modes once the mesh is up."),
		errorMessage: widget.NewLabel(""),
	}
	p.emptyNote.Wrapping = fyne.TextWrapWord
	p.errorMessage.Wrapping = fyne.TextWrapWord
	p.detailIdent.Wrapping = fyne.TextWrapWord

	p.list = widget.NewList(
		func() int { return len(p.peers) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < 0 || i >= len(p.peers) {
				return
			}
			o.(*widget.Label).SetText(rosterLabel(p.peers[i]))
		},
	)
	p.list.OnSelected = func(id widget.ListItemID) {
		p.selected = int(id)
		p.applyDetail()
	}

	chartCard := widget.NewCard("Latency", "round-trip time, most recent on the right", p.chart)
	p.detailWrap = container.NewVBox(
		p.detailName,
		p.detailBadge,
		p.detailIdent,
		p.detailRTT,
		chartCard,
	)

	// Left: roster list. Right: detail (or an empty note when no peers).
	detailScroll := container.NewVScroll(p.detailWrap)
	split := container.NewHSplit(p.list, container.NewStack(detailScroll, p.emptyNote))
	split.Offset = 0.32

	refresh := widget.NewButton("Refresh", func() { p.Refresh() })
	footer := container.NewVBox(p.errorMessage, refresh)
	p.root = container.NewBorder(nil, footer, nil, nil, split)

	p.showEmpty(true)
	return p
}

func (p *devicesPane) Content() fyne.CanvasObject { return p.root }

// Refresh polls the daemon for the peer set, folds each measured peer's
// RTT into its local history, and re-renders the roster + detail. The
// IPC fetch runs on the calling goroutine (window.go's poll spawns it
// off-main); every widget mutation is marshalled back to the Fyne main
// goroutine via fyne.Do (strict thread-checker, F-108).
func (p *devicesPane) Refresh() {
	if p.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peers, err := p.client.GetPeerConnectivity(ctx)
	if err != nil {
		fyne.Do(func() { p.errorMessage.SetText("Failed to fetch connectivity: " + err.Error()) })
		return
	}
	fyne.Do(func() {
		p.errorMessage.SetText("")
		p.apply(peers)
	})
}

// apply updates the pane from a fresh peer set.
func (p *devicesPane) apply(peers []ipc.PeerConnectivity) {
	// Fold each measured peer's latest RTT into its rolling history, and
	// forget peers that are gone.
	seen := map[string]bool{}
	for _, pc := range peers {
		seen[pc.IP] = true
		if pc.Measured {
			h := p.history[pc.IP]
			h = append(h, pc.RTTLastMs)
			if len(h) > devicesHistoryCap {
				h = h[len(h)-devicesHistoryCap:]
			}
			p.history[pc.IP] = h
		}
	}
	for ip := range p.history {
		if !seen[ip] {
			delete(p.history, ip)
		}
	}

	p.peers = peers
	p.list.Refresh()

	if len(peers) == 0 {
		p.selected = -1
		p.showEmpty(true)
		return
	}
	p.showEmpty(false)
	if p.selected < 0 || p.selected >= len(peers) {
		p.selected = 0
		p.list.Select(0)
	}
	p.applyDetail()
}

// applyDetail renders the selected peer into the detail widgets.
func (p *devicesPane) applyDetail() {
	if p.selected < 0 || p.selected >= len(p.peers) {
		return
	}
	pc := p.peers[p.selected]

	p.detailName.SetText(peerName(pc))
	p.detailBadge.SetText(badgeText(pc))

	ident := fmt.Sprintf("IP: %s", pc.IP)
	if pc.FQDN != "" {
		ident += "\nName: " + pc.FQDN
	}
	if pc.PubKey != "" {
		ident += "\nKey: " + truncKey(pc.PubKey)
	}
	if pc.Path == "relayed" && pc.RelayAddress != "" {
		ident += "\nRelay: " + pc.RelayAddress
	}
	if pc.LocalICEType != "" || pc.RemoteICEType != "" {
		ident += fmt.Sprintf("\nICE: %s / %s", dash(pc.LocalICEType), dash(pc.RemoteICEType))
	}
	p.detailIdent.SetText(ident)

	if pc.Measured {
		p.detailRTT.SetText(fmt.Sprintf(
			"RTT  last %.1f ms   avg %.1f ms   min %.1f ms   max %.1f ms   loss %.0f%%   (n=%d)",
			pc.RTTLastMs, pc.RTTAvgMs, pc.RTTMinMs, pc.RTTMaxMs, pc.LossPct, pc.Samples))
	} else {
		// Honest: not yet measured, no fabricated 0.
		p.detailRTT.SetText("RTT  — (measuring…)")
	}
	p.chart.setData(p.history[pc.IP])
}

func (p *devicesPane) showEmpty(empty bool) {
	if empty {
		p.emptyNote.Show()
		p.detailWrap.Hide()
	} else {
		p.emptyNote.Hide()
		p.detailWrap.Show()
	}
}

// --- rendering helpers ---

func peerName(pc ipc.PeerConnectivity) string {
	if pc.FQDN != "" {
		return pc.FQDN
	}
	return pc.IP
}

// rosterLabel is the one-line roster entry: a state dot + the peer name.
func rosterLabel(pc ipc.PeerConnectivity) string {
	dot := "○" // offline / not connected
	if pc.Connected {
		if pc.Path == "relayed" {
			dot = "◆" // relayed
		} else {
			dot = "●" // direct
		}
	}
	return dot + " " + peerName(pc)
}

// badgeText is the human direct/relayed/offline badge for the detail view.
func badgeText(pc ipc.PeerConnectivity) string {
	if !pc.Connected {
		return "○ Offline"
	}
	if pc.Path == "relayed" {
		return "◆ Relayed connection"
	}
	return "● Direct connection"
}

func truncKey(k string) string {
	if len(k) <= 16 {
		return k
	}
	return k[:16] + "…"
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
