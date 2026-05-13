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
}

// NewFake returns a fresh Fake in StateClosed.
func NewFake() *Fake { return &Fake{state: StateClosed, peerCnt: 3} }

func (f *Fake) Configure(cfg Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("innermesh: closed")
	}
	f.cfg = cfg
	return nil
}

func (f *Fake) Connect(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errors.New("innermesh: closed")
	}
	f.state = StateConfiguring
	f.mu.Unlock()

	// Simulate a short bring-up; bail if ctx is cancelled.
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		f.mu.Lock()
		f.state = StateClosed
		f.mu.Unlock()
		return ctx.Err()
	}
	f.mu.Lock()
	f.state = StateUp
	f.upAt = time.Now()
	f.mu.Unlock()
	return nil
}

func (f *Fake) Disconnect(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = StateClosed
	f.upAt = time.Time{}
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

func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.state = StateClosed
	return nil
}
