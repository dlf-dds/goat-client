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
	peers   []PeerStatus
	logs    []string
	maxLogs int
}

// fakeLogCap is the Fake's log ring-buffer capacity. Big enough for
// the GUI's Diagnostics pane to show recent activity, small enough
// not to grow without bound.
const fakeLogCap = 256

// fakePeers returns a small, realistic synthetic peer set: a mix of
// direct and relayed paths so the connectivity-check panel and its tests
// exercise both badge states. peerCnt stays in sync with its length.
func fakePeers() []PeerStatus {
	return []PeerStatus{
		{IP: "100.92.0.11", PubKey: "fakepubkey-alpha", FQDN: "alpha.goat", Connected: true, Relayed: false, LocalICEType: "host", RemoteICEType: "srflx", LastHandshake: time.Now().Add(-20 * time.Second), BytesRx: 4096, BytesTx: 2048, Latency: 8 * time.Millisecond},
		{IP: "100.92.0.12", PubKey: "fakepubkey-bravo", FQDN: "bravo.goat", Connected: true, Relayed: false, LocalICEType: "srflx", RemoteICEType: "srflx", LastHandshake: time.Now().Add(-35 * time.Second), BytesRx: 8192, BytesTx: 1024, Latency: 21 * time.Millisecond},
		{IP: "100.92.0.13", PubKey: "fakepubkey-charlie", FQDN: "charlie.goat", Connected: true, Relayed: true, LocalICEType: "relay", RemoteICEType: "relay", RelayAddress: "relay.goat:33073", LastHandshake: time.Now().Add(-50 * time.Second), BytesRx: 512, BytesTx: 512, Latency: 0},
	}
}

// NewFake returns a fresh Fake in StateClosed.
func NewFake() *Fake {
	p := fakePeers()
	return &Fake{state: StateClosed, peerCnt: len(p), peers: p, maxLogs: fakeLogCap}
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

// Peers returns the synthetic peer set when up, or an empty slice when
// not — mirroring the real Netbird.Peers contract. The returned slice is
// a copy, safe to read without the lock.
func (f *Fake) Peers() ([]PeerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateUp {
		return nil, nil
	}
	out := make([]PeerStatus, len(f.peers))
	copy(out, f.peers)
	return out, nil
}

// LocalIP returns a synthetic local overlay IP when up, "" otherwise.
func (f *Fake) LocalIP() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateUp {
		return "", nil
	}
	return "100.92.0.1", nil
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
