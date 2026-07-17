package names

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// Forwarder is the device-wide fallback resolver: a local UDP DNS server
// that answers A/AAAA queries live-upstream-first (never shadowed), then
// from the verified snapshot, then from the noncanonical observed tier —
// every fallback answer logged with its provenance label.
//
// It binds loopback only. Pointing the OS at it (scutil match-domains on
// macOS, resolved routing domains on Linux) is the tunnel/dns adapter's
// job and is wired per-mode by the daemon; the forwarder itself is
// mode-agnostic.
type Forwarder struct {
	store     *Store
	upstreams func() []string // e.g. ["100.64.165.203:5353"]; re-read per query
	timeout   time.Duration

	// rebindDelays paces listener recovery after a serve crash. The
	// forwarder may be fronting OS mesh-zone resolution (ADR 1082
	// unification), so it is supervised: crash → rebind with backoff;
	// exhausted backoff → unhealthy (onState(false), the daemon fails
	// open to direct mesh DNS) while retries continue at the last
	// delay; recovery → healthy again (onState(true)).
	rebindDelays []time.Duration

	mu      sync.Mutex
	server  *dns.Server
	addr    string
	healthy bool
	onState func(up bool)

	lastFall atomic.Int64 // unix seconds of the last fallback-served answer
	served   atomic.Int64 // total fallback answers served
}

// NewForwarder builds a forwarder over the given store. upstreams is
// consulted per query so the daemon can swap mesh nameservers at
// runtime (e.g. after re-enrollment) without a restart.
func NewForwarder(store *Store, upstreams func() []string, timeout time.Duration) *Forwarder {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Forwarder{
		store:        store,
		upstreams:    upstreams,
		timeout:      timeout,
		rebindDelays: []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second},
	}
}

// SetOnStateChange registers the health transition callback: up=false
// when the forwarder loses its listener and cannot immediately get it
// back, up=true when it recovers. Called from the supervise goroutine —
// implementations must not block. Set before Start.
func (f *Forwarder) SetOnStateChange(fn func(up bool)) {
	f.mu.Lock()
	f.onState = fn
	f.mu.Unlock()
}

// Start binds the UDP listener (addr like "127.0.0.1:53535") and serves
// until ctx is cancelled, supervised: a crashed listener is rebound with
// backoff, and health transitions fire the SetOnStateChange callback.
// Returns the bound address (useful with :0).
func (f *Forwarder) Start(ctx context.Context, addr string) (string, error) {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return "", fmt.Errorf("names forwarder bind %s: %w", addr, err)
	}
	bound := conn.LocalAddr().String()
	f.mu.Lock()
	f.addr = bound
	f.healthy = true
	f.mu.Unlock()
	go f.supervise(ctx, bound, conn)
	return bound, nil
}

// supervise serves on conn, and on unexpected serve failure rebinds the
// same address with backoff. Exhausting the backoff ladder marks the
// forwarder unhealthy (the daemon fails open to direct mesh DNS) while
// retries continue at the final delay; a successful rebind marks it
// healthy again.
func (f *Forwarder) supervise(ctx context.Context, bound string, conn net.PacketConn) {
	for {
		srv := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(f.handle)}
		f.mu.Lock()
		f.server = srv
		f.mu.Unlock()
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = srv.Shutdown()
			case <-done:
			}
		}()
		err := srv.ActivateAndServe()
		close(done)
		if ctx.Err() != nil {
			return
		}
		log.Printf("names forwarder: serve ended unexpectedly: %v — rebinding %s", err, bound)

		conn = nil
		for i := 0; conn == nil; i++ {
			delay := f.rebindDelays[min(i, len(f.rebindDelays)-1)]
			if i == len(f.rebindDelays) {
				// Ladder exhausted — fail open to live DNS while we keep
				// trying at the final delay.
				f.setHealthy(false)
				log.Printf("names forwarder: cannot rebind %s — marked unhealthy (OS falls back to direct mesh DNS); retrying every %s", bound, delay)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			c, err := net.ListenPacket("udp", bound)
			if err != nil {
				continue
			}
			conn = c
		}
		f.setHealthy(true)
	}
}

// setHealthy records a health transition and fires the callback on change.
func (f *Forwarder) setHealthy(up bool) {
	f.mu.Lock()
	changed := f.healthy != up
	f.healthy = up
	fn := f.onState
	f.mu.Unlock()
	if changed && fn != nil {
		fn(up)
	}
}

// Addr returns the bound address ("" before Start).
func (f *Forwarder) Addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addr
}

// Healthy reports whether the forwarder currently holds its listener.
func (f *Forwarder) Healthy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy
}

// FallbackStats reports (total fallback answers, time of the last one).
func (f *Forwarder) FallbackStats() (int64, time.Time) {
	ts := f.lastFall.Load()
	var t time.Time
	if ts > 0 {
		t = time.Unix(ts, 0)
	}
	return f.served.Load(), t
}

func (f *Forwarder) handle(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 {
		reply := new(dns.Msg)
		reply.SetRcode(req, dns.RcodeFormatError)
		_ = w.WriteMsg(reply)
		return
	}
	q := req.Question[0]

	// 1. Live upstream — never shadowed. Any definitive upstream answer
	// (including NXDOMAIN) is relayed verbatim; only no-answer-at-all
	// (dead upstreams, timeouts) falls through.
	if resp := f.tryUpstreams(req); resp != nil {
		if resp.Rcode == dns.RcodeSuccess && len(resp.Answer) > 0 {
			f.recordLiveAnswers(resp)
			_ = w.WriteMsg(resp)
			return
		}
		if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeServerFailure {
			_ = w.WriteMsg(resp)
			return
		}
		// SERVFAIL or empty success → treat as name-plane failure and
		// consult the fallback tiers below.
	}

	// 2/3. Signed snapshot, then observed tier — A/AAAA only.
	if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
		reply := new(dns.Msg)
		reply.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(reply)
		return
	}
	now := time.Now()
	snap, snapErr := f.store.LoadSnapshot()
	// Verified self-claim first (ADR 1082 Amendment 2): signed by the
	// name's own anchor, authorized by the snapshot's bindings — peer
	// names only by construction. Demotes the observed tier wherever a
	// valid claim answers.
	if snapErr == nil {
		if vc := f.store.LookupClaim(q.Name, snap, now); vc != nil {
			wantV6 := q.Qtype == dns.TypeAAAA
			if vc.IP.Is6() == wantV6 {
				log.Printf("names forwarder: %s → %s via verified SELF-CLAIM, age %s (mesh DNS unreachable; signed by the name's own anchor)",
					q.Name, vc.IP, AgeHuman(vc.Age))
				f.served.Add(1)
				f.lastFall.Store(now.Unix())
				reply := new(dns.Msg)
				reply.SetReply(req)
				hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: 30}
				if wantV6 {
					hdr.Rrtype = dns.TypeAAAA
					reply.Answer = append(reply.Answer, &dns.AAAA{Hdr: hdr, AAAA: vc.IP.AsSlice()})
				} else {
					hdr.Rrtype = dns.TypeA
					reply.Answer = append(reply.Answer, &dns.A{Hdr: hdr, A: vc.IP.AsSlice()})
				}
				_ = w.WriteMsg(reply)
				return
			}
		}
	}
	obs := f.store.LookupObserved(q.Name, now)
	ans, err := PickFallback(snap, snapErr, obs, q.Name, now)
	if err != nil {
		log.Printf("names forwarder: %s unresolvable (live upstreams unreachable; %v)", q.Name, err)
		reply := new(dns.Msg)
		reply.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(reply)
		return
	}
	wantV6 := q.Qtype == dns.TypeAAAA
	if ans.IP.Is6() != wantV6 {
		// Record family doesn't match the question: answer empty success
		// so the client retries the other family rather than erroring.
		reply := new(dns.Msg)
		reply.SetReply(req)
		_ = w.WriteMsg(reply)
		return
	}
	switch ans.Source {
	case SourceSnapshot:
		label := fmt.Sprintf("signed snapshot serial %d, age %s", ans.Meta.Serial, AgeHuman(ans.Meta.Age))
		if ans.Meta.Grade == GradeAging {
			label += " — STALE (aging)"
		}
		log.Printf("names forwarder: %s → %s via %s (mesh DNS unreachable)", q.Name, ans.IP, label)
	case SourceObserved:
		log.Printf("names forwarder: %s → %s via NONCANONICAL (ad hoc) observed record, age %s (mesh DNS unreachable; codify this record)",
			q.Name, ans.IP, AgeHuman(ans.ObservedAge))
	}
	f.served.Add(1)
	f.lastFall.Store(now.Unix())

	reply := new(dns.Msg)
	reply.SetReply(req)
	hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: 30}
	if wantV6 {
		hdr.Rrtype = dns.TypeAAAA
		reply.Answer = append(reply.Answer, &dns.AAAA{Hdr: hdr, AAAA: ans.IP.AsSlice()})
	} else {
		hdr.Rrtype = dns.TypeA
		reply.Answer = append(reply.Answer, &dns.A{Hdr: hdr, A: ans.IP.AsSlice()})
	}
	_ = w.WriteMsg(reply)
}

// tryUpstreams forwards the query to each upstream in order and returns
// the first response, or nil when none answered inside the budget.
func (f *Forwarder) tryUpstreams(req *dns.Msg) *dns.Msg {
	client := &dns.Client{Net: "udp", Timeout: f.timeout}
	for _, up := range f.upstreams() {
		up = strings.TrimSpace(up)
		if up == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(up); err != nil {
			up = net.JoinHostPort(up, "53")
		}
		resp, _, err := client.Exchange(req, up)
		if err == nil && resp != nil {
			return resp
		}
	}
	return nil
}

// recordLiveAnswers feeds the observed tier from a live response.
func (f *Forwarder) recordLiveAnswers(resp *dns.Msg) {
	now := time.Now()
	for _, rr := range resp.Answer {
		switch a := rr.(type) {
		case *dns.A:
			if ip, ok := netip.AddrFromSlice(a.A.To4()); ok {
				_ = f.store.RecordObservation(a.Hdr.Name, ip.String(), now)
			}
		case *dns.AAAA:
			if ip, ok := netip.AddrFromSlice(a.AAAA); ok {
				_ = f.store.RecordObservation(a.Hdr.Name, ip.String(), now)
			}
		}
	}
}
