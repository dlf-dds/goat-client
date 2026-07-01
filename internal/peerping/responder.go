package peerping

import (
	"context"
	"errors"
	"net"
	"time"
)

// Responder echoes peer-ping probes back to their sender. Every
// goat-client runs one bound to the peer's own tunnel IP so that other
// peers can measure the round-trip time to it. It only ever echoes
// well-formed current-version probes verbatim — unrelated traffic that
// lands on the port is read and dropped, never reflected.
//
// The zero value is usable. The read-loop is cancelled via the context;
// the Responder never closes a PacketConn it did not open.
type Responder struct {
	// readTick bounds how long a blocked read waits before re-checking
	// the context, so Serve shuts down promptly on cancel. 0 ⇒ 1s.
	readTick time.Duration
}

func (r *Responder) tick() time.Duration {
	if r.readTick > 0 {
		return r.readTick
	}
	return time.Second
}

// Serve reads probes on conn and echoes each valid one back to its
// source until ctx is cancelled. On cancellation it returns the
// context's error (context.Canceled / DeadlineExceeded); it returns some
// other non-nil error only on an unexpected socket failure. Serve does
// not close conn.
func (r *Responder) Serve(ctx context.Context, conn net.PacketConn) error {
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Bound the blocking read so cancellation is observed even when
		// the socket is idle.
		_ = conn.SetReadDeadline(time.Now().Add(r.tick()))
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // idle tick; loop and re-check ctx
			}
			// A real read error while shutting down means the socket was
			// closed under us as part of cancellation — report that as
			// the context error, not as a fault.
			return ctxErr(ctx, err)
		}
		if _, decErr := decodeProbe(buf[:n]); decErr != nil {
			continue // not a probe we recognize; drop it
		}
		// Echo the validated probe verbatim. A failed write to one peer
		// must not tear down the responder for every other peer.
		_ = conn.SetWriteDeadline(time.Now().Add(r.tick()))
		_, _ = conn.WriteTo(buf[:n], addr)
	}
}

// ListenAndServe binds a UDP PacketConn on addr (host:port; an empty
// host binds all interfaces, but callers should pass the peer's tunnel
// IP to keep it mesh-only) and serves until ctx is cancelled. It closes
// the socket it opened before returning. Like Serve, it returns the
// context's error on cancellation.
func (r *Responder) ListenAndServe(ctx context.Context, addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Unblock a read that is parked past a cancel by closing the socket.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	return r.Serve(ctx, conn)
}

// ctxErr collapses an error that occurred while ctx was already
// cancelled into the context's error, so shutdown-induced socket
// failures read as cancellation rather than as a fault.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
