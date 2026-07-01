package peerping

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// timeoutError is a net.Error that reports itself as a timeout, so
// fakeConn can simulate a read deadline elapsing with no real waiting.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var dummyAddr net.Addr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51822}

// fakeConn is a deterministic net.PacketConn: ReadFrom serves preloaded
// frames in order, then reports a timeout (empty queue = deadline
// reached). WriteTo records what was sent. No real sockets, no sleeps —
// it isolates Probe's sequence-matching logic from timing.
type fakeConn struct {
	mu      sync.Mutex
	frames  [][]byte
	written [][]byte
	closed  bool
}

func (c *fakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, net.ErrClosed
	}
	if len(c.frames) == 0 {
		return 0, nil, timeoutError{}
	}
	f := c.frames[0]
	c.frames = c.frames[1:]
	return copy(b, f), dummyAddr, nil
}

func (c *fakeConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, append([]byte(nil), b...))
	return len(b), nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}
func (c *fakeConn) LocalAddr() net.Addr            { return dummyAddr }
func (*fakeConn) SetDeadline(time.Time) error      { return nil }
func (*fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeConn) SetWriteDeadline(time.Time) error { return nil }

func TestProbeMatchedEcho(t *testing.T) {
	conn := &fakeConn{frames: [][]byte{encodeProbe(1)}}
	p := &Pinger{Timeout: ms(50)}
	s, err := p.Probe(context.Background(), conn, dummyAddr, 1)
	if err != nil {
		t.Fatalf("Probe err = %v", err)
	}
	if s.Lost {
		t.Fatalf("matched echo recorded Lost, want a hit")
	}
	if s.Seq != 1 {
		t.Fatalf("Seq = %d, want 1", s.Seq)
	}
	// The probe was actually written to the wire.
	if len(conn.written) != 1 {
		t.Fatalf("wrote %d probes, want 1", len(conn.written))
	}
	if got, _ := decodeProbe(conn.written[0]); got != 1 {
		t.Fatalf("written probe seq = %d, want 1", got)
	}
}

func TestProbeIgnoresStaleAndJunkThenMatches(t *testing.T) {
	conn := &fakeConn{frames: [][]byte{
		encodeProbe(6),        // stale echo from an earlier probe
		[]byte("not-a-probe"), // junk on the port
		encodeProbe(7),        // the reply we're waiting for
	}}
	p := &Pinger{Timeout: ms(50)}
	s, err := p.Probe(context.Background(), conn, dummyAddr, 7)
	if err != nil {
		t.Fatalf("Probe err = %v", err)
	}
	if s.Lost || s.Seq != 7 {
		t.Fatalf("Sample = %+v, want a hit on seq 7", s)
	}
}

func TestProbeTimeoutIsLostNotError(t *testing.T) {
	conn := &fakeConn{} // empty queue → ReadFrom reports timeout
	p := &Pinger{Timeout: ms(50)}
	s, err := p.Probe(context.Background(), conn, dummyAddr, 3)
	if err != nil {
		t.Fatalf("Probe on timeout returned err = %v, want nil (loss is not an error)", err)
	}
	if !s.Lost {
		t.Fatalf("timed-out probe Lost = false, want true")
	}
	if s.Seq != 3 {
		t.Fatalf("Seq = %d, want 3", s.Seq)
	}
}

func TestProbeContextCancel(t *testing.T) {
	conn := &fakeConn{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Pinger{Timeout: time.Minute}
	if _, err := p.Probe(ctx, conn, dummyAddr, 1); err != context.Canceled {
		t.Fatalf("Probe err = %v, want context.Canceled", err)
	}
}
