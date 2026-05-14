package innermesh

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Fake is an in-process Mesh implementation that drives the daemon's
// state-machine without doing any real network work. Used until Worker
// A's real Block 76N lands; also useful in unit tests for the GUI +
// mode-reconciliation surface in 76O/76P.
type Fake struct {
	mu      sync.Mutex
	cfg     Config
	state   State
	upAt    time.Time
	closed  bool
	peerCnt int
	logs    []string
	maxLogs int
}

// fakeLogCap is the Fake's log ring-buffer capacity. Big enough for
// the GUI's Diagnostics pane to show recent activity, small enough
// not to grow without bound.
const fakeLogCap = 256

// NewFake returns a fresh Fake in StateClosed.
func NewFake() *Fake {
	return &Fake{state: StateClosed, peerCnt: 3, maxLogs: fakeLogCap}
}

func (f *Fake) Configure(cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("innermesh: closed")
	}
	f.cfg = cfg
	f.appendLogLocked("configure: profile applied")
	return nil
}

func (f *Fake) Connect(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errors.New("innermesh: closed")
	}
	f.state = StateConfiguring
	f.appendLogLocked("connect: configuring")
	f.mu.Unlock()

	// Simulate a short bring-up; bail if ctx is cancelled.
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		f.mu.Lock()
		f.state = StateClosed
		f.appendLogLocked("connect: ctx canceled mid-connect")
		f.mu.Unlock()
		return ctx.Err()
	}
	f.mu.Lock()
	f.state = StateUp
	f.upAt = time.Now()
	f.appendLogLocked("connect: state=up")
	f.mu.Unlock()
	return nil
}

func (f *Fake) Disconnect(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = StateClosed
	f.upAt = time.Time{}
	f.appendLogLocked("disconnect: state=closed")
	return nil
}

func (f *Fake) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *Fake) Stats() (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateUp {
		return Stats{}, nil
	}
	uptime := time.Since(f.upAt)
	return Stats{
		PeerCount:     f.peerCnt,
		BytesIn:       uint64(uptime.Seconds()) * 1024,
		BytesOut:      uint64(uptime.Seconds()) * 256,
		LastHandshake: time.Now().Add(-30 * time.Second),
	}, nil
}

// Logs returns up to tail trailing log lines from the in-memory ring
// buffer. tail <= 0 returns the entire buffer.
func (f *Fake) Logs(tail int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tail <= 0 || tail > len(f.logs) {
		out := make([]string, len(f.logs))
		copy(out, f.logs)
		return out
	}
	out := make([]string, tail)
	copy(out, f.logs[len(f.logs)-tail:])
	return out
}

func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.state = StateClosed
	f.appendLogLocked("close: released")
	return nil
}

// appendLogLocked writes one log line; caller holds f.mu.
func (f *Fake) appendLogLocked(msg string) {
	line := time.Now().UTC().Format(time.RFC3339) + " " + msg
	f.logs = append(f.logs, line)
	if len(f.logs) > f.maxLogs {
		f.logs = f.logs[len(f.logs)-f.maxLogs:]
	}
}
