package peerping

import (
	"context"
	"net"
	"testing"
	"time"
)

// peerTarget starts a standalone Responder (a stand-in peer) on loopback
// and returns a Target pointing at it plus a stop func.
func peerTarget(t *testing.T) (Target, func()) {
	t.Helper()
	addr, stop := startResponder(t)
	ua := addr.(*net.UDPAddr)
	return Target{IP: ua.IP.String(), Port: ua.Port}, stop
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
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

func newTestManager() *Manager {
	return NewManager(ManagerConfig{
		BindAddr: "127.0.0.1:0", // own responder on an ephemeral port
		Interval: 20 * time.Millisecond,
		Timeout:  time.Second,
		History:  32,
	})
}

func TestManagerMeasuresTarget(t *testing.T) {
	tgt, stop := peerTarget(t)
	defer stop()

	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.SetTargets([]Target{tgt})

	// The sampler should accumulate non-lost samples for the peer.
	waitFor(t, 3*time.Second, func() bool {
		return m.Snapshot()[tgt.IP].N >= 3
	})
	st := m.Snapshot()[tgt.IP]
	if st.Lost != 0 {
		t.Fatalf("measured %d losses against a live responder, want 0", st.Lost)
	}
	if samples, ok := m.Samples(tgt.IP); !ok || len(samples) < 3 {
		t.Fatalf("Samples(%s) = %d,%v, want ≥3 series points", tgt.IP, len(samples), ok)
	}
}

func TestManagerReconcileAddsAndRemoves(t *testing.T) {
	// a is a real loopback peer (fast hits); b is a distinct, unreachable
	// overlay IP — real mesh peers have distinct IPs, which is the key the
	// Manager reconciles on. b's reachability is irrelevant here; the test
	// is about the sampler set tracking the target set by key.
	a, stopA := peerTarget(t)
	defer stopA()
	b := Target{IP: "127.0.0.9", Port: 51899}

	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Both samplers exist as soon as SetTargets returns (the key is added
	// with an empty ring before the first probe lands).
	m.SetTargets([]Target{a, b})
	s := m.Snapshot()
	if _, ok := s[a.IP]; !ok {
		t.Fatalf("peer a not tracked after SetTargets")
	}
	if _, ok := s[b.IP]; !ok {
		t.Fatalf("peer b not tracked after SetTargets")
	}

	// Drop b: its sampler stops and it leaves the snapshot; a remains.
	m.SetTargets([]Target{a})
	if _, present := m.Snapshot()[b.IP]; present {
		t.Fatalf("peer b still tracked after removal")
	}
	if _, ok := m.Samples(b.IP); ok {
		t.Fatalf("Samples(%s) still present after removal", b.IP)
	}
	if _, ok := m.Snapshot()[a.IP]; !ok {
		t.Fatalf("peer a dropped by the b-removal reconcile")
	}

	// a keeps measuring for real.
	waitFor(t, 3*time.Second, func() bool { return m.Snapshot()[a.IP].N >= 1 })
}

func TestManagerSamplesUnknownPeer(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)
	if _, ok := m.Samples("100.64.0.99"); ok {
		t.Fatal("Samples for an unmeasured peer returned ok=true")
	}
}

func TestManagerSetTargetsBeforeStartIsNoop(t *testing.T) {
	m := newTestManager()
	m.SetTargets([]Target{{IP: "100.64.0.1"}})
	if n := len(m.Snapshot()); n != 0 {
		t.Fatalf("SetTargets before Start started %d samplers, want 0", n)
	}
}

func TestManagerIgnoresEmptyIP(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)
	m.SetTargets([]Target{{IP: ""}}) // empty-IP target must be ignored
	if n := len(m.Snapshot()); n != 0 {
		t.Fatalf("empty-IP target created %d samplers, want 0", n)
	}
	m.Stop()
}

func TestManagerStartTwiceErrors(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := m.Start(ctx); err == nil {
		t.Fatal("second Start returned nil, want an already-started error")
	}
}

func TestManagerStopIsIdempotent(t *testing.T) {
	m := newTestManager()
	ctx := context.Background()
	_ = m.Start(ctx)
	m.Stop()
	m.Stop() // must not panic or block
	// After Stop, SetTargets is a no-op.
	m.SetTargets([]Target{{IP: "100.64.0.1"}})
	if n := len(m.Snapshot()); n != 0 {
		t.Fatalf("SetTargets after Stop started %d samplers, want 0", n)
	}
}
