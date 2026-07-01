package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

func TestDevicesPane_EmptyShowsNote(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConn = nil
	p := newDevicesPane(fc)
	p.Refresh()

	if !p.emptyNote.Visible() {
		t.Fatal("empty note should be visible with no peers")
	}
	if p.detailWrap.Visible() {
		t.Fatal("detail should be hidden with no peers")
	}
}

func TestDevicesPane_RostersPeers(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConn = []ipc.PeerConnectivity{
		{IP: "100.92.0.11", FQDN: "alpha.goat", Connected: true, Path: "direct", Measured: true, RTTLastMs: 8, RTTAvgMs: 9, RTTMinMs: 7, RTTMaxMs: 12, Samples: 5},
		{IP: "100.92.0.13", FQDN: "charlie.goat", Connected: true, Path: "relayed"},
	}
	p := newDevicesPane(fc)
	p.Refresh()

	if len(p.peers) != 2 {
		t.Fatalf("peers=%d want 2", len(p.peers))
	}
	if p.emptyNote.Visible() {
		t.Fatal("empty note should be hidden when peers exist")
	}
	// First peer auto-selected → detail renders its badge + RTT.
	if got := p.detailBadge.Text; got != "● Direct connection" {
		t.Errorf("badge = %q, want direct", got)
	}
	if p.detailRTT.Text == "" || p.detailRTT.Text == "RTT  — (measuring…)" {
		t.Errorf("measured peer should show RTT stats, got %q", p.detailRTT.Text)
	}
}

func TestDevicesPane_RelayedBadgeAndUnmeasured(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConn = []ipc.PeerConnectivity{
		{IP: "100.92.0.13", FQDN: "charlie.goat", Connected: true, Path: "relayed", RelayAddress: "relay.goat:33073", Measured: false},
	}
	p := newDevicesPane(fc)
	p.Refresh()

	if got := p.detailBadge.Text; got != "◆ Relayed connection" {
		t.Errorf("badge = %q, want relayed", got)
	}
	// Honest: no samples yet → measuring, not a fake 0.
	if got := p.detailRTT.Text; got != "RTT  — (measuring…)" {
		t.Errorf("unmeasured RTT = %q, want the measuring placeholder", got)
	}
}

func TestDevicesPane_HistoryAccumulatesAndFeedsChart(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	base := ipc.PeerConnectivity{IP: "100.92.0.11", Connected: true, Path: "direct", Measured: true}

	p := newDevicesPane(fc)
	for i, rtt := range []float64{8, 9, 7} {
		b := base
		b.RTTLastMs = rtt
		b.Samples = i + 1
		fc.peerConn = []ipc.PeerConnectivity{b}
		p.Refresh()
	}

	h := p.history["100.92.0.11"]
	if len(h) != 3 {
		t.Fatalf("history len=%d want 3 (one per refresh)", len(h))
	}
	if h[0] != 8 || h[2] != 7 {
		t.Errorf("history order wrong: %v", h)
	}
	if len(p.chart.data) != 3 {
		t.Errorf("chart data len=%d want 3", len(p.chart.data))
	}
}

func TestDevicesPane_ForgetsDepartedPeer(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()

	p := newDevicesPane(fc)
	fc.peerConn = []ipc.PeerConnectivity{{IP: "100.92.0.11", Connected: true, Path: "direct", Measured: true, RTTLastMs: 8}}
	p.Refresh()
	if _, ok := p.history["100.92.0.11"]; !ok {
		t.Fatal("expected history for the present peer")
	}
	// Peer disappears → its history is dropped.
	fc.peerConn = nil
	p.Refresh()
	if _, ok := p.history["100.92.0.11"]; ok {
		t.Fatal("history for a departed peer should be forgotten")
	}
}

func TestDevicesPane_SurfacesError(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConnErr = errBoom
	p := newDevicesPane(fc)
	p.Refresh()
	if p.errorMessage.Text == "" {
		t.Fatal("expected an error message on a failed fetch")
	}
}

func TestDevicesPane_SendFileInvokesClientWithPeerIP(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConn = []ipc.PeerConnectivity{{IP: "100.92.0.11", FQDN: "alpha.goat", Connected: true, Path: "direct"}}
	fc.sendFileReply = ipc.SendFileReply{Name: "doc.pdf", Size: 42, ToPeer: "alpha.goat"}
	p := newDevicesPane(fc)
	p.Refresh() // selects peer 0

	p.sendFileTo(p.peers[0], "/home/me/doc.pdf")

	if len(fc.sendFileCalls) != 1 {
		t.Fatalf("SendFile calls = %d, want 1", len(fc.sendFileCalls))
	}
	if fc.sendFileCalls[0][0] != "100.92.0.11" || fc.sendFileCalls[0][1] != "/home/me/doc.pdf" {
		t.Fatalf("SendFile args = %v, want peer IP + path", fc.sendFileCalls[0])
	}
	if p.sendStatus.Text == "" || p.sendStatus.Text[:4] != "Sent" {
		t.Errorf("send status = %q, want a success line", p.sendStatus.Text)
	}
}

func TestDevicesPane_SendFileSurfacesError(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConn = []ipc.PeerConnectivity{{IP: "100.92.0.11", Connected: true, Path: "direct"}}
	fc.sendFileErr = errBoom
	p := newDevicesPane(fc)
	p.Refresh()

	p.sendFileTo(p.peers[0], "/x")
	if p.sendStatus.Text == "" || p.sendStatus.Text[:4] != "Send" {
		t.Errorf("send status = %q, want a failure line", p.sendStatus.Text)
	}
}

func TestDevicesPane_ReceivedListRenders(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	fc.peerConn = []ipc.PeerConnectivity{{IP: "100.92.0.11", Connected: true, Path: "direct"}}
	fc.incoming = []ipc.IncomingFile{
		{Name: "map.tiff", Size: 2048, From: "alpha.goat"},
		{Name: "notes.txt", Size: 12, FromIP: "100.92.0.12"},
	}
	p := newDevicesPane(fc)
	p.Refresh()

	got := p.recvLabel.Text
	if !strings.Contains(got, "map.tiff") || !strings.Contains(got, "alpha.goat") {
		t.Errorf("received label missing an entry: %q", got)
	}
	if !strings.Contains(got, "100.92.0.12") { // FromIP fallback when From is empty
		t.Errorf("received label should fall back to FromIP: %q", got)
	}
}

func TestDevicesPane_ReceivedEmpty(t *testing.T) {
	test.NewTempApp(t)
	fc := newFakeClient()
	p := newDevicesPane(fc)
	p.Refresh()
	if p.recvLabel.Text != "none yet" {
		t.Errorf("empty received label = %q, want \"none yet\"", p.recvLabel.Text)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1048576: "1.0 MB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
