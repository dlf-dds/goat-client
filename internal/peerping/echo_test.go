package peerping

import (
	"context"
	"net"
	"testing"
	"time"
)

// startResponder binds a Responder on 127.0.0.1 and returns its address
// plus a stop func. It is the in-test stand-in for a peer running
// goat-client's echo responder.
func startResponder(t *testing.T) (net.Addr, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r := &Responder{readTick: 50 * time.Millisecond}
	go func() {
		defer close(done)
		_ = r.Serve(ctx, conn)
	}()
	stop := func() {
		cancel()
		_ = conn.Close()
		<-done
	}
	return conn.LocalAddr(), stop
}

// TestEchoRoundTripReal is the end-to-end fast-validation: a real
// Responder echoes a real Pinger's probe over the loopback and the
// Pinger records a non-lost sample with a measured RTT.
func TestEchoRoundTripReal(t *testing.T) {
	addr, stop := startResponder(t)
	defer stop()

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client ListenPacket: %v", err)
	}
	defer client.Close()

	p := &Pinger{Timeout: 2 * time.Second}
	s, err := p.Probe(context.Background(), client, addr, 1)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if s.Lost {
		t.Fatalf("real loopback probe was Lost, want a hit")
	}
	// RTT must be non-negative. It can legitimately be exactly 0 on
	// platforms whose monotonic clock is coarser than the round trip
	// (Windows time.Now() has ~1ms resolution, and a loopback echo is
	// faster than that) — a 0 reading is a valid sub-resolution
	// measurement, not a failure. The success signal is !Lost, above.
	if s.RTT < 0 {
		t.Fatalf("real loopback RTT = %v, want >= 0", s.RTT)
	}
	if s.Seq != 1 {
		t.Fatalf("Seq = %d, want 1", s.Seq)
	}
}

// TestProbeNoResponderTimesOut points a Pinger at a closed loopback port
// and confirms the probe is recorded as loss (not an error) within the
// timeout.
func TestProbeNoResponderTimesOut(t *testing.T) {
	// Bind then immediately close to get a very-likely-dead port.
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	deadAddr := dead.LocalAddr()
	_ = dead.Close()

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client ListenPacket: %v", err)
	}
	defer client.Close()

	p := &Pinger{Timeout: 150 * time.Millisecond}
	start := time.Now()
	s, err := p.Probe(context.Background(), client, deadAddr, 1)
	if err != nil {
		t.Fatalf("Probe err = %v, want nil", err)
	}
	if !s.Lost {
		t.Fatalf("probe to dead port Lost = false, want true")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probe took %v, want ~timeout (150ms)", elapsed)
	}
}

// TestRunFeedsRing drives the Run loop against a live Responder and
// confirms it accumulates samples into the ring on its cadence.
func TestRunFeedsRing(t *testing.T) {
	addr, stop := startResponder(t)
	defer stop()

	ring := NewRing(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &Pinger{Interval: 20 * time.Millisecond, Timeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, addr.String(), ring) }()

	// Wait for at least 3 samples to land, then stop.
	deadline := time.After(3 * time.Second)
	for ring.Len() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d samples after 3s, want ≥3", ring.Len())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned err = %v", err)
	}

	st := ring.Stats()
	if st.N < 3 {
		t.Fatalf("Stats.N = %d, want ≥3", st.N)
	}
	if st.Lost != 0 {
		t.Fatalf("loopback Run recorded %d losses, want 0", st.Lost)
	}
	// Avg is non-negative; it can be 0 when every loopback round trip
	// measured under the platform's clock resolution (see the RTT note in
	// TestEchoRoundTripReal). The signal here is samples accumulating with
	// no loss, not a positive average.
	if st.Avg < 0 {
		t.Fatalf("Stats.Avg = %v, want >= 0", st.Avg)
	}
}
