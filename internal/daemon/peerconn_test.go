package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/mode"
	"github.com/dlf-dds/goat-client/internal/peerping"
)

// stubSnap is a peerConnSource returning canned RTT stats, so the daemon's
// join logic can be tested without a live peerping subsystem.
type stubSnap struct{ stats map[string]peerping.Stats }

func (s stubSnap) Snapshot() map[string]peerping.Stats { return s.stats }

// The Fake mesh's synthetic peers (see innermesh.fakePeers): .11 and .12
// are direct, .13 is relayed.
const (
	fakePeerDirectIP  = "100.92.0.11"
	fakePeerRelayedIP = "100.92.0.13"
)

func TestGetPeerConnectivityJoinsBadgeAndRTT(t *testing.T) {
	d := newTestDaemon(t, mode.NetbirdOnly)
	if err := d.mesh.Connect(context.Background()); err != nil {
		t.Fatalf("mesh connect: %v", err)
	}
	// Measure only the direct peer; the others have no samples yet.
	d.peerConn = stubSnap{stats: map[string]peerping.Stats{
		fakePeerDirectIP: {N: 10, Last: 8 * time.Millisecond, Avg: 9 * time.Millisecond, Min: 7 * time.Millisecond, Max: 12 * time.Millisecond, LossPct: 0},
	}}

	reply, err := d.GetPeerConnectivity(context.Background())
	if err != nil {
		t.Fatalf("GetPeerConnectivity: %v", err)
	}
	if len(reply.Peers) == 0 {
		t.Fatal("no peers returned; want the Fake synthetic set")
	}

	byIP := map[string]int{}
	for i, p := range reply.Peers {
		byIP[p.IP] = i
	}

	// Direct peer: badge from the mesh, RTT from the stub.
	di, ok := byIP[fakePeerDirectIP]
	if !ok {
		t.Fatalf("direct peer %s missing from reply", fakePeerDirectIP)
	}
	dp := reply.Peers[di]
	if dp.Path != "direct" {
		t.Errorf("direct peer Path=%q want \"direct\"", dp.Path)
	}
	if !dp.Measured {
		t.Errorf("direct peer should be Measured (stub has samples)")
	}
	if dp.Samples != 10 || dp.RTTAvgMs != 9 || dp.RTTMinMs != 7 || dp.RTTMaxMs != 12 || dp.RTTLastMs != 8 {
		t.Errorf("direct peer RTT join mismatch: %+v", dp)
	}

	// Relayed peer: badge present, but no RTT samples → not Measured, zero RTT.
	ri, ok := byIP[fakePeerRelayedIP]
	if !ok {
		t.Fatalf("relayed peer %s missing from reply", fakePeerRelayedIP)
	}
	rp := reply.Peers[ri]
	if rp.Path != "relayed" {
		t.Errorf("relayed peer Path=%q want \"relayed\"", rp.Path)
	}
	if rp.Measured || rp.RTTAvgMs != 0 {
		t.Errorf("unmeasured peer should have Measured=false + zero RTT: %+v", rp)
	}
}

func TestGetPeerConnectivityNilSourceIsUnmeasured(t *testing.T) {
	d := newTestDaemon(t, mode.Combined)
	if err := d.mesh.Connect(context.Background()); err != nil {
		t.Fatalf("mesh connect: %v", err)
	}
	// d.peerConn stays nil — the join must still return peers, all unmeasured.
	reply, err := d.GetPeerConnectivity(context.Background())
	if err != nil {
		t.Fatalf("GetPeerConnectivity: %v", err)
	}
	if len(reply.Peers) == 0 {
		t.Fatal("want peers even with a nil RTT source")
	}
	for _, p := range reply.Peers {
		if p.Measured {
			t.Errorf("peer %s Measured with a nil source", p.IP)
		}
	}
}

func TestGetPeerConnectivityEmptyWithoutInnerMesh(t *testing.T) {
	d := newTestDaemon(t, mode.WGCP0Only)
	reply, err := d.GetPeerConnectivity(context.Background())
	if err != nil {
		t.Fatalf("GetPeerConnectivity: %v", err)
	}
	if len(reply.Peers) != 0 {
		t.Fatalf("wg-cp0-only mode returned %d peers, want 0", len(reply.Peers))
	}
}
