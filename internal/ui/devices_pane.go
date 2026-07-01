package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
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

	// goatdrop widgets
	window     fyne.Window
	sendBtn    *widget.Button
	sendStatus *widget.Label
	received   []ipc.IncomingFile
	recvLabel  *widget.Label
}

// SetWindow wires the pane to its parent window so the file picker + error
// dialogs have a parent. Called by the window before first show.
func (p *devicesPane) SetWindow(w fyne.Window) { p.window = w }

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
		sendStatus:   widget.NewLabel(""),
		recvLabel:    widget.NewLabel("none yet"),
	}
	p.emptyNote.Wrapping = fyne.TextWrapWord
	p.errorMessage.Wrapping = fyne.TextWrapWord
	p.detailIdent.Wrapping = fyne.TextWrapWord
	p.sendStatus.Wrapping = fyne.TextWrapWord
	p.recvLabel.Wrapping = fyne.TextWrapWord
	p.sendBtn = widget.NewButton("Send file…", func() { p.openSendPicker() })

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
	dropCard := widget.NewCard("File drop (goatdrop)", "send a file to this peer over the mesh",
		container.NewVBox(p.sendBtn, p.sendStatus))
	p.detailWrap = container.NewVBox(
		p.detailName,
		p.detailBadge,
		p.detailIdent,
		p.detailRTT,
		chartCard,
		dropCard,
	)

	// Left: roster list. Right: detail (or an empty note when no peers).
	detailScroll := container.NewVScroll(p.detailWrap)
	split := container.NewHSplit(p.list, container.NewStack(detailScroll, p.emptyNote))
	split.Offset = 0.32

	recvCard := widget.NewCard("Received (goatdrop)", "", p.recvLabel)
	refresh := widget.NewButton("Refresh", func() { p.Refresh() })
	footer := container.NewVBox(recvCard, p.errorMessage, refresh)
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
	incoming, ierr := p.client.GetIncomingFiles(ctx)
	fyne.Do(func() {
		p.errorMessage.SetText("")
		p.apply(peers)
		if ierr == nil {
			p.received = incoming
			p.renderReceived()
		}
	})
}

// openSendPicker prompts for a local file and drops it to the selected
// peer over goatdrop. The daemon reads the file by path (both run on this
// host), so only the path is needed, not the bytes.
func (p *devicesPane) openSendPicker() {
	if p.window == nil || p.selected < 0 || p.selected >= len(p.peers) {
		return
	}
	peer := p.peers[p.selected]
	d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, p.window)
			return
		}
		if rc == nil {
			return
		}
		path := rc.URI().Path()
		_ = rc.Close()
		p.sendStatus.SetText("Sending " + filepath.Base(path) + "…")
		go p.sendFileTo(peer, path)
	}, p.window)
	if home, err := os.UserHomeDir(); err == nil {
		if uri, err := storage.ListerForURI(storage.NewFileURI(home)); err == nil {
			d.SetLocation(uri)
		}
	}
	d.Show()
}

// sendFileTo drops path to peer via the daemon, then reports the outcome in
// the send-status label. Blocks on the transfer; openSendPicker runs it in a
// goroutine, and widget mutations are marshalled back via fyne.Do.
func (p *devicesPane) sendFileTo(peer ipc.PeerConnectivity, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	res, err := p.client.SendFile(ctx, peer.IP, path)
	fyne.Do(func() {
		if err != nil {
			p.sendStatus.SetText("Send failed: " + err.Error())
			return
		}
		p.sendStatus.SetText(fmt.Sprintf("Sent %s (%d bytes) to %s", res.Name, res.Size, peerName(peer)))
	})
}

// renderReceived renders the recent-inbound list (capped) into recvLabel.
func (p *devicesPane) renderReceived() {
	if len(p.received) == 0 {
		p.recvLabel.SetText("none yet")
		return
	}
	var b strings.Builder
	for i, f := range p.received {
		if i >= 8 {
			break
		}
		from := f.From
		if from == "" {
			from = f.FromIP
		}
		fmt.Fprintf(&b, "%s (%s) — from %s\n", f.Name, humanSize(f.Size), from)
	}
	p.recvLabel.SetText(strings.TrimRight(b.String(), "\n"))
}

// humanSize renders a byte count compactly (B / KB / MB / GB).
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
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
