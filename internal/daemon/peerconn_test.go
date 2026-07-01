package daemon

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/innermesh"
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

// fakePeerConn is a peerConnController stand-in that records the reconcile
// loop's calls without binding real sockets.
type fakePeerConn struct {
	mu       sync.Mutex
	bindAddr string
	started  bool
	stopped  bool
	targets  []peerping.Target
}

func (f *fakePeerConn) Start(context.Context) error {
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return nil
}
func (f *fakePeerConn) SetTargets(t []peerping.Target) {
	f.mu.Lock()
	f.targets = t
	f.mu.Unlock()
}
func (f *fakePeerConn) Snapshot() map[string]peerping.Stats { return nil }
func (f *fakePeerConn) Stop()                               { f.mu.Lock(); f.stopped = true; f.mu.Unlock() }

func (f *fakePeerConn) snap() (started, stopped bool, n int, bind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.stopped, len(f.targets), f.bindAddr
}

func daemonWithFakePeerConn(t *testing.T, initial mode.Mode, fc *fakePeerConn) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{
		BundlePath:                filepath.Join(dir, "bundle.cbor"),
		SocketPath:                filepath.Join(dir, "ipc.sock"),
		ConfigPath:                filepath.Join(dir, "config.toml"),
		InitialMode:               initial,
		InnerMeshFactory:          func() innermesh.Mesh { return innermesh.NewFake() },
		PeerConnReconcileInterval: 20 * time.Millisecond,
		PeerConnFactory: func(bindAddr string) peerConnController {
			fc.mu.Lock()
			fc.bindAddr = bindAddr
			fc.mu.Unlock()
			return fc
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func waitCond(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(d)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("condition not met within %v", d)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestPeerConnReconcileTurnsOnWithMesh drives runPeerConn against a live
// (Fake) mesh and confirms it starts the subsystem, binds to the local
// tunnel IP, feeds it the peer set, and stops it when the mesh goes down.
func TestPeerConnReconcileTurnsOnWithMesh(t *testing.T) {
	fc := &fakePeerConn{}
	d := daemonWithFakePeerConn(t, mode.NetbirdOnly, fc)
	if err := d.mesh.Connect(context.Background()); err != nil {
		t.Fatalf("mesh connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runPeerConn(ctx)

	// Subsystem starts, binds to the Fake's local IP + peerping port, and
	// receives the 3 synthetic peers as targets.
	waitCond(t, 3*time.Second, func() bool {
		started, _, n, _ := fc.snap()
		return started && n == 3
	})
	wantBind := net.JoinHostPort("100.92.0.1", strconv.Itoa(peerping.DefaultPort))
	if _, _, _, bind := fc.snap(); bind != wantBind {
		t.Fatalf("bind addr = %q, want %q (local tunnel IP + peerping port)", bind, wantBind)
	}
	// It is published as the daemon's RTT source.
	d.mu.RLock()
	haveSrc := d.peerConn != nil
	d.mu.RUnlock()
	if !haveSrc {
		t.Fatal("peerConn source not published after start")
	}

	// Bring the mesh down: the loop tears the subsystem down.
	if err := d.mesh.Disconnect(context.Background()); err != nil {
		t.Fatalf("mesh disconnect: %v", err)
	}
	waitCond(t, 3*time.Second, func() bool {
		_, stopped, _, _ := fc.snap()
		return stopped
	})
	d.mu.RLock()
	clearedSrc := d.peerConn == nil
	d.mu.RUnlock()
	if !clearedSrc {
		t.Fatal("peerConn source not cleared after mesh down")
	}
}

// TestPeerConnReconcileStaysOffWithoutInnerMesh confirms the loop never
// starts the subsystem in a mode that excludes the inner mesh.
func TestPeerConnReconcileStaysOffWithoutInnerMesh(t *testing.T) {
	fc := &fakePeerConn{}
	d := daemonWithFakePeerConn(t, mode.WGCP0Only, fc)

	ctx, cancel := context.WithCancel(context.Background())
	go d.runPeerConn(ctx)
	time.Sleep(120 * time.Millisecond) // several reconcile ticks
	cancel()

	if started, _, _, _ := fc.snap(); started {
		t.Fatal("subsystem started in wg-cp0-only mode")
	}
}
