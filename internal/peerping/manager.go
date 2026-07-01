package peerping

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

// Target is a peer to measure: its overlay (tunnel) IP and the port its
// Responder listens on. Port 0 means DefaultPort.
type Target struct {
	IP   string
	Port int
}

func (t Target) port() int {
	if t.Port > 0 {
		return t.Port
	}
	return DefaultPort
}

func (t Target) addr() string {
	return net.JoinHostPort(t.IP, strconv.Itoa(t.port()))
}

// ManagerConfig configures a Manager. The zero value is usable once
// BindAddr is set; the timing fields fall back to the package defaults.
type ManagerConfig struct {
	// BindAddr is where this node's own Responder listens (host:port).
	// host should be the node's tunnel IP to keep it mesh-only; an empty
	// host binds all interfaces. Port is normally DefaultPort so peers
	// can find it. An empty BindAddr disables the inbound Responder (the
	// Manager still measures outbound to peers that run one).
	BindAddr string
	Interval time.Duration // per-peer probe cadence; 0 ⇒ DefaultInterval
	Timeout  time.Duration // per-probe timeout; 0 ⇒ DefaultTimeout
	History  int           // per-peer ring capacity; 0 ⇒ DefaultHistory
}

// Manager runs the peer-ping subsystem for the daemon: one Responder so
// other peers can measure this node, and one Pinger+Ring per target peer
// so this node measures them. It reconciles the running Pinger set to
// match SetTargets, so the daemon can drive it straight off the live
// inner-mesh peer list. The connectivity-check panel reads Snapshot /
// Samples.
//
// All methods are safe for concurrent use. Start must be called once
// before SetTargets; the whole subsystem stops when the Start context is
// cancelled or Stop is called.
type Manager struct {
	cfg ManagerConfig

	mu      sync.Mutex
	ctx     context.Context
	started bool
	stopped bool
	conn    net.PacketConn // owned Responder socket, nil when BindAddr empty
	peers   map[string]*peerRunner
}

// peerRunner is one target's live sampler: its Ring plus the cancel for
// its Pinger goroutine.
type peerRunner struct {
	target Target
	ring   *Ring
	cancel context.CancelFunc
}

// NewManager returns a Manager for cfg. Start it before use.
func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{cfg: cfg, peers: make(map[string]*peerRunner)}
}

// Start binds and launches this node's Responder (unless BindAddr is
// empty) and records the context that bounds every per-peer sampler.
// Bind errors surface here. When ctx is cancelled the Responder and all
// samplers stop. Start is idempotent-safe only in that a second call
// returns an error rather than double-starting.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("peerping: Manager already started")
	}
	m.started = true
	m.ctx = ctx

	if m.cfg.BindAddr != "" {
		conn, err := net.ListenPacket("udp", m.cfg.BindAddr)
		if err != nil {
			return err
		}
		m.conn = conn
		r := &Responder{}
		go func() {
			<-ctx.Done()
			_ = conn.Close()
		}()
		go func() { _ = r.Serve(ctx, conn) }()
	}

	// Tear everything down on context cancel.
	go func() {
		<-ctx.Done()
		m.Stop()
	}()
	return nil
}

// SetTargets reconciles the running per-peer samplers to exactly the
// given targets: it starts a sampler for each newly-seen peer and stops
// the sampler for each peer no longer present (dropping its history).
// Peers still present keep their running sampler and accumulated Ring.
// Targets are keyed by IP; a target with an empty IP is ignored.
//
// It is safe to call SetTargets repeatedly on a timer against the live
// inner-mesh peer list. It is a no-op before Start or after Stop.
func (m *Manager) SetTargets(targets []Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.stopped {
		return
	}

	wanted := make(map[string]Target, len(targets))
	for _, t := range targets {
		if t.IP == "" {
			continue
		}
		wanted[t.IP] = t
	}

	// Stop samplers for peers that disappeared.
	for ip, pr := range m.peers {
		if _, ok := wanted[ip]; !ok {
			pr.cancel()
			delete(m.peers, ip)
		}
	}

	// Start samplers for newly-seen peers.
	for ip, t := range wanted {
		if _, ok := m.peers[ip]; ok {
			continue
		}
		ring := NewRing(m.cfg.History)
		pctx, cancel := context.WithCancel(m.ctx)
		pinger := &Pinger{Interval: m.cfg.Interval, Timeout: m.cfg.Timeout, History: m.cfg.History}
		go func(addr string) { _ = pinger.Run(pctx, addr, ring) }(t.addr())
		m.peers[ip] = &peerRunner{target: t, ring: ring, cancel: cancel}
	}
}

// Snapshot returns each measured peer's current rolling Stats, keyed by
// peer IP. The map is a fresh copy safe to hold.
func (m *Manager) Snapshot() map[string]Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Stats, len(m.peers))
	for ip, pr := range m.peers {
		out[ip] = pr.ring.Stats()
	}
	return out
}

// Samples returns the rolling sample history for one peer (oldest first),
// or nil and false when that peer is not currently measured. This is the
// series a latency graph plots.
func (m *Manager) Samples(ip string) ([]Sample, bool) {
	m.mu.Lock()
	pr, ok := m.peers[ip]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	return pr.ring.Samples(), true
}

// Stop cancels every per-peer sampler and closes the Responder socket.
// Idempotent. Called automatically when the Start context is cancelled.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	for ip, pr := range m.peers {
		pr.cancel()
		delete(m.peers, ip)
	}
	if m.conn != nil {
		_ = m.conn.Close()
		m.conn = nil
	}
}
