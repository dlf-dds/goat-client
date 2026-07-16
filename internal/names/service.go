package names

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

// RefreshInterval paces the opportunistic snapshot refresh. The artifact
// changes only when the site registry changes, so hourly is generous.
const RefreshInterval = time.Hour

// Service composes the store, the refresh loop, and the UDP forwarder
// into the daemon-owned names subsystem (design §4.1/§4.3: the daemon is
// the sole refresher of the shared store; other readers — goat-cli —
// verify the same bytes).
type Service struct {
	store *Store
	fwd   *Forwarder

	mu        sync.RWMutex
	upstreams []string
	siteFn    func() (site, zone string)
}

// StatusSnapshot is the names block surfaced over IPC — the honesty
// contract rendered as data (serial + age + grade, observed count,
// fallback-served counters).
type StatusSnapshot struct {
	Grade          string    `json:"grade"` // fresh|aging|expired|unavailable
	Serial         uint64    `json:"serial,omitempty"`
	Age            string    `json:"age,omitempty"`
	Records        int       `json:"records"`
	Observed       int       `json:"observed"`
	ForwarderAddr  string    `json:"forwarderAddr,omitempty"`
	FallbackServed int64     `json:"fallbackServed"`
	LastFallback   time.Time `json:"lastFallback,omitempty"`
}

// NewService opens the store at dir (verifying against roots — the same
// trust roots the daemon uses for enrollment bundles) and prepares the
// forwarder. Call SetUpstreams/SetSite as the daemon learns them; call
// Run to start.
func NewService(dir string, roots []*ecdsa.PublicKey) (*Service, error) {
	store, err := NewStore(dir, roots)
	if err != nil {
		return nil, err
	}
	s := &Service{store: store}
	s.fwd = NewForwarder(store, s.currentUpstreams, 2*time.Second)
	return s, nil
}

// Store exposes the underlying store (the daemon seeds observations from
// its own successful connections if it wants to).
func (s *Service) Store() *Store { return s.store }

// ForwarderAddr returns the forwarder's bound address ("" before Run).
func (s *Service) ForwarderAddr() string { return s.fwd.Addr() }

// ForwarderHealthy reports whether the forwarder currently holds its
// listener — the gate for fronting OS mesh-zone resolution at it.
func (s *Service) ForwarderHealthy() bool { return s.fwd.Healthy() }

// SetForwarderStateFunc registers the forwarder health-transition
// callback (see Forwarder.SetOnStateChange). Set before Run.
func (s *Service) SetForwarderStateFunc(fn func(up bool)) { s.fwd.SetOnStateChange(fn) }

// SetUpstreams records the mesh nameservers the OS is being pointed at —
// the forwarder's live tier. Safe to call at every (re)connect.
func (s *Service) SetUpstreams(servers []string) {
	s.mu.Lock()
	s.upstreams = append([]string(nil), servers...)
	s.mu.Unlock()
}

// SetSiteFunc wires the provider of the goatnet identity used to derive
// the refresh origin (get.<site>.<zone>) — typically a closure over the
// daemon's current bundle, so a bundle imported after start is picked up
// on the next refresh tick with no re-wiring. zone is the mesh DNS zone
// (for goat deployments the management hostname, e.g.
// netbird.efdi-backbone.net).
func (s *Service) SetSiteFunc(fn func() (site, zone string)) {
	s.mu.Lock()
	s.siteFn = fn
	s.mu.Unlock()
}

func (s *Service) currentUpstreams() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.upstreams...)
}

func (s *Service) baseURL() string {
	s.mu.RLock()
	fn := s.siteFn
	s.mu.RUnlock()
	if fn == nil {
		return ""
	}
	return GetBaseURL(fn())
}

// Run starts the forwarder on listenAddr and the refresh loop, until ctx
// is cancelled. Refresh is best-effort and only meaningful while names
// (and the get tier) work — failures log at debug volume and never
// affect serving.
func (s *Service) Run(ctx context.Context, listenAddr string) error {
	addr, err := s.fwd.Start(ctx, listenAddr)
	if err != nil {
		return err
	}
	log.Printf("names: forwarder listening on %s (live-first; signed-snapshot + observed fallback)", addr)
	go s.refreshLoop(ctx)
	return nil
}

func (s *Service) refreshLoop(ctx context.Context) {
	client := &http.Client{Timeout: RefreshBudget}
	tick := time.NewTicker(RefreshInterval)
	defer tick.Stop()
	attempt := func() {
		base := s.baseURL()
		if base == "" {
			return // no bundle/site yet — nothing to derive the origin from
		}
		if _, err := s.store.Refresh(ctx, client, base); err != nil && !errors.Is(err, ErrNotNewer) {
			log.Printf("names: refresh from %s skipped: %v", base, err)
		}
	}
	// One eager attempt at start (covers daemon restart with a live mesh),
	// then the ticker.
	attempt()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			attempt()
		}
	}
}

// StatusSnapshot renders the current names state for getStatus.
func (s *Service) StatusSnapshot(now time.Time) StatusSnapshot {
	out := StatusSnapshot{Grade: "unavailable"}
	if snap, err := s.store.LoadSnapshot(); err == nil {
		meta := snap.GradeAt(now)
		out.Grade = string(meta.Grade)
		out.Serial = meta.Serial
		out.Age = AgeHuman(meta.Age)
		out.Records = meta.Records
	}
	out.Observed = s.store.ObservedCount(now)
	out.ForwarderAddr = s.fwd.Addr()
	out.FallbackServed, out.LastFallback = s.fwd.FallbackStats()
	return out
}
