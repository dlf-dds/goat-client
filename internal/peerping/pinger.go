package peerping

import (
	"context"
	"errors"
	"net"
	"time"
)

// Pinger measures round-trip latency to a single peer's Responder by
// sending probes and timing their echoes. It feeds a Ring so the UI/IPC
// layer can render a live graph. The zero value is usable; defaults
// apply.
//
// RTT is measured locally with a monotonic clock (start-to-echo), so no
// timestamp rides the wire and peer clock skew never distorts the
// reading. Probes are sequenced; a reply carrying an older sequence
// (a late echo from a previous, already-timed-out probe) is ignored so
// it cannot be mistaken for the current probe's reply.
type Pinger struct {
	Interval time.Duration // gap between probes in Run; 0 ⇒ DefaultInterval
	Timeout  time.Duration // per-probe wait for the echo; 0 ⇒ DefaultTimeout
	History  int           // Ring capacity when Run makes its own; 0 ⇒ DefaultHistory

	// Test seams, unexported to keep the public API small.
	now func() time.Time
}

func (p *Pinger) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}

func (p *Pinger) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultTimeout
}

func (p *Pinger) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// Probe sends one probe with the given sequence number to target over
// conn and waits for the matching echo. It returns a completed Sample:
// on a matched echo, Lost is false and RTT is the round-trip time; on a
// timeout, Lost is true and RTT is zero. A non-nil error is returned
// only for an unexpected socket failure (a plain timeout is not an
// error — it is a lost Sample).
func (p *Pinger) Probe(ctx context.Context, conn net.PacketConn, target net.Addr, seq uint64) (Sample, error) {
	timeout := p.timeout()
	start := p.clock()

	if _, err := conn.WriteTo(encodeProbe(seq), target); err != nil {
		return Sample{}, err
	}

	deadline := start.Add(timeout)
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return Sample{}, ctx.Err()
		}
		remaining := deadline.Sub(p.clock())
		if remaining <= 0 {
			return Sample{Seq: seq, At: p.clock(), Lost: true}, nil
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return Sample{Seq: seq, At: p.clock(), Lost: true}, nil
			}
			if ctx.Err() != nil {
				return Sample{}, ctx.Err()
			}
			return Sample{}, err
		}
		gotSeq, decErr := decodeProbe(buf[:n])
		if decErr != nil || gotSeq != seq {
			// Junk on the port, or a late echo from an earlier probe.
			// Keep waiting for this probe's own reply until the deadline.
			continue
		}
		return Sample{Seq: seq, At: p.clock(), RTT: p.clock().Sub(start)}, nil
	}
}

// Run probes target once immediately, then every Interval, appending
// each Sample to ring until ctx is cancelled. It dials a connected UDP
// socket to target and closes it on return. target is a host:port on the
// peer's tunnel IP.
//
// Run blocks; callers spawn it as a goroutine per peer. A transient
// send/receive error is recorded as a lost Sample and the loop
// continues — a peer flapping in and out should show as loss on the
// graph, not stop the sampler.
func (p *Pinger) Run(ctx context.Context, target string, ring *Ring) error {
	raddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return err
	}
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return err
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	var seq uint64
	probe := func() {
		seq++
		s, err := p.Probe(ctx, conn, raddr, seq)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Unexpected socket error: record loss and keep sampling.
			s = Sample{Seq: seq, At: p.clock(), Lost: true}
		}
		ring.Add(s)
	}

	probe()
	t := time.NewTicker(p.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			probe()
		}
	}
}
